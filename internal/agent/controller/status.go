package controller

import (
	"context"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"eggbox/internal/agentapi"
)

// state is the mutable part of the status. All access goes through update/read
// so every change is published exactly once with a new sequence number.
type state struct {
	service           agentapi.ServiceState
	conn              agentapi.ConnectionState
	activeRev         uint64
	intentConnected   bool
	lastErrCode       agentapi.ErrorCode
	lastErr           string
	connectedSince    time.Time
	cleanup           agentapi.CleanupState
	cleanupDetail     string
	network           agentapi.NetworkState
	mode              string
	tunIface          string
	warnings          []string
	reconnectAttempts uint32
	sandboxPort       uint32
	// fingerprint identifies the uplink the running connection was built on.
	fingerprint string
}

type statusSub struct {
	ch chan *agentapi.Status
}

func (c *Controller) initialState() state {
	return state{
		service:           agentapi.ServiceState_SERVICE_STATE_STARTING,
		conn:              agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED,
		cleanup:           agentapi.CleanupState_CLEANUP_STATE_IDLE,
		network:           agentapi.NetworkState_NETWORK_STATE_ONLINE,
		mode:              "",
		reconnectAttempts: 0,
	}
}

// update mutates the state under the lock and publishes the result.
func (c *Controller) update(fn func(*state)) {
	c.mu.Lock()
	fn(&c.st)
	c.seq++
	snap := c.snapshotLocked()
	subs := make([]*statusSub, 0, len(c.subs))
	for s := range c.subs {
		subs = append(subs, s)
	}
	c.mu.Unlock()

	for _, s := range subs {
		// Latest wins: replace a stale pending status instead of queueing a backlog.
		select {
		case s.ch <- snap:
		default:
			select {
			case <-s.ch:
			default:
			}
			select {
			case s.ch <- snap:
			default:
			}
		}
	}
}

func (c *Controller) snapshotLocked() *agentapi.Status {
	stored := c.cfg.Store.CurrentN()
	st := &c.st
	return &agentapi.Status{
		Seq:                c.seq,
		Service:            st.service,
		Connection:         st.conn,
		ActiveRevision:     st.activeRev,
		StoredRevision:     stored,
		IntentConnected:    st.intentConnected,
		LastErrorCode:      st.lastErrCode,
		LastError:          st.lastErr,
		ConnectedSinceUnix: unixOrZero(st.connectedSince),
		Cleanup:            st.cleanup,
		CleanupDetail:      st.cleanupDetail,
		Network:            st.network,
		Mode:               st.mode,
		TunInterface:       st.tunIface,
		Warnings:           append([]string(nil), st.warnings...),
		CoreVersion:        c.cfg.Engine.Version(),
		AgentVersion:       c.cfg.Version,
		AuthorizedUser:     c.cfg.AuthorizedUser,
		HasConfig:          stored != 0,
		ReconnectAttempts:  st.reconnectAttempts,
		UpdatedUnix:        time.Now().Unix(),
		SandboxPort:        st.sandboxPort,
	}
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// Status returns the current status.
func (c *Controller) Status() *agentapi.Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshotLocked()
}

// WatchStatus delivers the current status and then every change until ctx ends.
func (c *Controller) WatchStatus(ctx context.Context) <-chan *agentapi.Status {
	sub := &statusSub{ch: make(chan *agentapi.Status, 1)}
	out := make(chan *agentapi.Status, 1)

	c.mu.Lock()
	c.subs[sub] = struct{}{}
	first := c.snapshotLocked()
	c.mu.Unlock()
	sub.ch <- first

	go func() {
		defer close(out)
		defer func() {
			c.mu.Lock()
			delete(c.subs, sub)
			c.mu.Unlock()
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case st := <-sub.ch:
				select {
				case out <- st:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

// idempotency -------------------------------------------------------------

const (
	idemCapacity = 512
	idemTTL      = 10 * time.Minute
)

type idemEntry struct {
	done    chan struct{}
	result  *agentapi.OperationResult
	expires time.Time
}

type idemCache struct {
	mu      sync.Mutex
	entries map[string]*idemEntry
	order   []string
}

func newIdemCache() *idemCache { return &idemCache{entries: map[string]*idemEntry{}} }

// begin returns the existing entry for id (joined=true) or registers a new one.
func (c *idemCache) begin(id string, now time.Time) (e *idemEntry, joined bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[id]; ok && (old.result == nil || now.Before(old.expires)) {
		return old, true
	}
	e = &idemEntry{done: make(chan struct{})}
	c.entries[id] = e
	c.order = append(c.order, id)
	for len(c.order) > idemCapacity {
		oldest := c.order[0]
		c.order = c.order[1:]
		if oe, ok := c.entries[oldest]; ok && oe.result != nil {
			delete(c.entries, oldest)
		}
	}
	return e, false
}

func (c *idemCache) finish(e *idemEntry, res *agentapi.OperationResult, now time.Time) {
	c.mu.Lock()
	e.result = proto.Clone(res).(*agentapi.OperationResult)
	e.expires = now.Add(idemTTL)
	c.mu.Unlock()
	close(e.done)
}

// replay returns a copy of a finished entry's result flagged as replayed.
func (e *idemEntry) replay() *agentapi.OperationResult {
	res := proto.Clone(e.result).(*agentapi.OperationResult)
	res.Replayed = true
	return res
}

// abort forgets a request that never ran and answers anyone waiting on it.
func (c *idemCache) abort(id string, e *idemEntry, res *agentapi.OperationResult) {
	c.mu.Lock()
	if c.entries[id] == e {
		delete(c.entries, id)
	}
	e.result = proto.Clone(res).(*agentapi.OperationResult)
	e.expires = time.Time{}
	c.mu.Unlock()
	close(e.done)
}
