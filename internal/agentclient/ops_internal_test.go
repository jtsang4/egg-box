package agentclient

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jtsang4/sing-box-ui/internal/agentapi"
)

// scripted answers Connect from a script, recording the request ids it saw.
type scripted struct {
	agentapi.AgentClient
	ids     []string
	answers []func() (*agentapi.OperationResult, error)
}

func (s *scripted) Connect(_ context.Context, in *agentapi.ConnectRequest, _ ...grpc.CallOption) (*agentapi.OperationResult, error) {
	s.ids = append(s.ids, in.RequestId)
	n := len(s.ids) - 1
	if n >= len(s.answers) {
		n = len(s.answers) - 1
	}
	return s.answers[n]()
}

func readyClient(api agentapi.AgentClient) *Client {
	c := New(nil, Options{})
	c.link = Link{State: LinkReady}
	c.api = api
	return c
}

func unavailable() (*agentapi.OperationResult, error) {
	return nil, status.Error(codes.Unavailable, "gone")
}

func TestDoRepeatsALostCallWithTheSameRequestID(t *testing.T) {
	api := &scripted{answers: []func() (*agentapi.OperationResult, error){
		unavailable,
		func() (*agentapi.OperationResult, error) {
			return &agentapi.OperationResult{Ok: true, Replayed: true}, nil
		},
	}}
	res, err := readyClient(api).Connect(context.Background(), ConnectOptions{})
	if err != nil || !res.Ok {
		t.Fatalf("res=%v err=%v", res, err)
	}
	if len(api.ids) != 2 || api.ids[0] == "" || api.ids[0] != api.ids[1] {
		t.Fatalf("request ids = %v: a repeat must reuse the id so the agent deduplicates", api.ids)
	}
}

func TestDoGivesUpAfterThreeLostCalls(t *testing.T) {
	api := &scripted{answers: []func() (*agentapi.OperationResult, error){unavailable}}
	_, err := readyClient(api).Connect(context.Background(), ConnectOptions{})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("err = %v", err)
	}
	if len(api.ids) != 3 {
		t.Fatalf("attempts = %d", len(api.ids))
	}
}

func TestDoDoesNotRepeatWhatCannotSucceed(t *testing.T) {
	api := &scripted{answers: []func() (*agentapi.OperationResult, error){
		func() (*agentapi.OperationResult, error) { return nil, status.Error(codes.InvalidArgument, "bad") },
	}}
	_, err := readyClient(api).Connect(context.Background(), ConnectOptions{})
	if status.Code(err) != codes.InvalidArgument || len(api.ids) != 1 {
		t.Fatalf("err=%v attempts=%d", err, len(api.ids))
	}

	// A refusal the agent explained is an answer, not a lost call.
	api = &scripted{answers: []func() (*agentapi.OperationResult, error){
		func() (*agentapi.OperationResult, error) {
			return &agentapi.OperationResult{
				Ok: false, ErrorCode: agentapi.ErrorCode_ERROR_CODE_ROUTE_CONFLICT, Message: "another VPN owns 10.0.0.0/8",
				Issues: []*agentapi.Issue{{Code: "x"}}, RolledBack: true,
			}, nil
		},
	}}
	res, err := readyClient(api).Connect(context.Background(), ConnectOptions{})
	var op *OpError
	if !errors.As(err, &op) || op.Code != agentapi.ErrorCode_ERROR_CODE_ROUTE_CONFLICT || len(op.Issues) != 1 || !op.RolledBack || res == nil {
		t.Fatalf("err = %v", err)
	}
	if len(api.ids) != 1 {
		t.Fatalf("a refused request was repeated %d times", len(api.ids))
	}
	if got := op.Error(); got != "another VPN owns 10.0.0.0/8 (the previous configuration is running again)" {
		t.Fatalf("message = %q", got)
	}
}

func TestDoWaitsForTheLinkBetweenAttempts(t *testing.T) {
	api := &scripted{answers: []func() (*agentapi.OperationResult, error){
		unavailable,
		func() (*agentapi.OperationResult, error) { return &agentapi.OperationResult{Ok: true}, nil },
	}}
	c := readyClient(api)
	// Drop the link right after the first failure and bring it back later.
	go func() {
		time.Sleep(30 * time.Millisecond)
		c.set(Link{State: LinkUnavailable, Err: errors.New("down")})
		time.Sleep(60 * time.Millisecond)
		c.mu.Lock()
		c.api = api
		c.mu.Unlock()
		c.set(Link{State: LinkReady})
	}()
	// The first call fails immediately; the retry must wait for the link.
	api.answers[0] = func() (*agentapi.OperationResult, error) {
		time.Sleep(50 * time.Millisecond)
		return unavailable()
	}
	res, err := c.Connect(context.Background(), ConnectOptions{})
	if err != nil || !res.Ok {
		t.Fatalf("res=%v err=%v", res, err)
	}
	if len(api.ids) != 2 {
		t.Fatalf("attempts = %d", len(api.ids))
	}
}

func TestNotReadyClientFailsFastWithTheLinkState(t *testing.T) {
	c := New(nil, Options{})
	c.link = Link{State: LinkRefused, Err: errors.New("refused")}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Connect(ctx, ConnectOptions{})
	if err == nil {
		t.Fatal("a call on a refused link must fail")
	}
	if _, aerr := c.API(); !errors.Is(aerr, ErrNotReady) {
		t.Fatalf("API: %v", aerr)
	}
}

func TestWaitReadyReturnsForPermanentStates(t *testing.T) {
	c := New(nil, Options{})
	c.link = Link{State: LinkIncompatible, Err: incompatibleError{agent: 7}}
	if err := c.WaitReady(context.Background()); err == nil || err.Error() == "" {
		t.Fatal("an incompatible agent never becomes ready; WaitReady must say so instead of waiting")
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	prev := time.Duration(0)
	for i := 0; i < 12; i++ {
		d := defaultBackoff(i)
		if d < prev || d > 3*time.Second {
			t.Fatalf("attempt %d: %v after %v", i, d, prev)
		}
		prev = d
	}
	if defaultBackoff(0) != 250*time.Millisecond || defaultBackoff(11) != 3*time.Second {
		t.Fatalf("backoff = %v .. %v", defaultBackoff(0), defaultBackoff(11))
	}
}

func TestRequestIDsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewRequestID()
		if len(id) != 32 || seen[id] {
			t.Fatalf("id %q (len %d) repeated or malformed", id, len(id))
		}
		seen[id] = true
	}
}
