package controller

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"eggbox/internal/agentapi"
)

// job is one unit of serialized work.
type job struct {
	kind          string
	requestID     string
	interruptible bool
	// internal jobs were not requested by a client and must run even if nobody waits for them.
	internal bool
	ctx      context.Context // the caller's context; only used to skip abandoned requests
	run      func(ctx context.Context) *agentapi.OperationResult
	entry    *idemEntry
	done     chan *agentapi.OperationResult
}

// finish records the result for idempotency and wakes the waiting caller.
func (j *job) finish(c *Controller, res *agentapi.OperationResult) {
	res.RequestId = j.requestID
	res.Revision = c.cfg.Store.CurrentN()
	c.mu.RLock()
	res.StatusSeq = c.seq
	c.mu.RUnlock()
	if j.entry != nil {
		c.idem.finish(j.entry, res, time.Now())
	}
	select {
	case j.done <- res:
	default:
	}
}

func (c *Controller) enqueue(j *job) bool {
	if j.done == nil {
		j.done = make(chan *agentapi.OperationResult, 1)
	}
	select {
	case c.queue <- j:
		return true
	default:
		return false
	}
}

// submit runs fn as a serialized job and waits for its result.
func (c *Controller) submit(ctx context.Context, requestID, kind string, interruptible bool, fn func(context.Context) *agentapi.OperationResult) *agentapi.OperationResult {
	if c.root == nil || c.closing.Load() {
		return failure(agentapi.ErrorCode_ERROR_CODE_UNAVAILABLE, "the agent is not accepting requests")
	}
	var entry *idemEntry
	if requestID != "" {
		e, joined := c.idem.begin(requestID, time.Now())
		if joined {
			select {
			case <-e.done:
				return e.replay()
			case <-ctx.Done():
				return cancelledWaiting(requestID)
			}
		}
		entry = e
	}

	j := &job{kind: kind, requestID: requestID, interruptible: interruptible, ctx: ctx, run: fn, entry: entry}
	if !c.enqueue(j) {
		res := failure(agentapi.ErrorCode_ERROR_CODE_BUSY, "too many operations are waiting; try again shortly")
		res.RequestId = requestID
		if entry != nil {
			// A BUSY answer must not be replayed forever: forget the request id.
			c.idem.abort(requestID, entry, res)
		}
		return res
	}
	select {
	case res := <-j.done:
		return res
	case <-ctx.Done():
		return cancelledWaiting(requestID)
	}
}

func cancelledWaiting(id string) *agentapi.OperationResult {
	res := failure(agentapi.ErrorCode_ERROR_CODE_CANCELLED, "the request was cancelled while waiting; repeat it with the same request id to learn the outcome")
	res.RequestId = id
	return res
}

// submitInternal queues work the agent decided on itself.
func (c *Controller) submitInternal(kind string, interruptible bool, fn func(context.Context) *agentapi.OperationResult) {
	if c.root == nil || c.closing.Load() {
		return
	}
	j := &job{kind: kind, interruptible: interruptible, internal: true, run: fn}
	if !c.enqueue(j) {
		c.log.Warn("dropping internal job, queue is full", "kind", kind)
	}
}

func (c *Controller) worker() {
	for {
		select {
		case <-c.root.Done():
			return
		case j := <-c.queue:
			c.runJob(j)
		}
	}
}

func (c *Controller) runJob(j *job) {
	if !j.internal && j.ctx != nil && j.ctx.Err() != nil {
		j.finish(c, failure(agentapi.ErrorCode_ERROR_CODE_CANCELLED, "the request was cancelled before it started"))
		return
	}
	jctx, cancel := context.WithCancel(c.root)
	c.curMu.Lock()
	c.curKind, c.curCancel, c.curInterruptible = j.kind, cancel, j.interruptible
	c.curMu.Unlock()

	var res *agentapi.OperationResult
	func() {
		defer func() {
			if r := recover(); r != nil {
				c.log.Error("controller job panicked", "kind", j.kind, "panic", r, "stack", string(debug.Stack()))
				res = failure(agentapi.ErrorCode_ERROR_CODE_INTERNAL, "internal error while running %s", j.kind)
			}
		}()
		res = j.run(jctx)
	}()
	if res == nil {
		res = &agentapi.OperationResult{Ok: true}
	}

	c.curMu.Lock()
	c.curKind, c.curCancel, c.curInterruptible = "", nil, false
	c.curMu.Unlock()
	cancel()
	j.finish(c, res)
}

// interrupt cancels the running job if it is one that a disconnect may preempt.
func (c *Controller) interrupt() {
	c.curMu.Lock()
	defer c.curMu.Unlock()
	if c.curCancel != nil && c.curInterruptible {
		c.curCancel()
	}
}

// -------------------------------------------------------------------- results

func failure(code agentapi.ErrorCode, format string, args ...any) *agentapi.OperationResult {
	return &agentapi.OperationResult{Ok: false, ErrorCode: code, Message: fmt.Sprintf(format, args...)}
}

func success(format string, args ...any) *agentapi.OperationResult {
	return &agentapi.OperationResult{Ok: true, Message: fmt.Sprintf(format, args...)}
}

// opError is a failed step inside an operation, convertible to a result.
type opError struct {
	code    agentapi.ErrorCode
	msg     string
	issues  []*agentapi.Issue
	details map[string]string
	cause   error
}

func (e *opError) Error() string {
	if e.cause != nil {
		return e.msg + ": " + e.cause.Error()
	}
	return e.msg
}

func newOpError(code agentapi.ErrorCode, cause error, format string, args ...any) *opError {
	return &opError{code: code, msg: fmt.Sprintf(format, args...), cause: cause}
}

func (e *opError) result() *agentapi.OperationResult {
	res := failure(e.code, "%s", e.Error())
	res.Issues = e.issues
	res.Details = e.details
	return res
}
