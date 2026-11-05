package urltestgroup

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

// slowDialer stands for a proxy node: opening a connection through it costs
// handshakes (delay), using an open one does not.
type slowDialer struct {
	addr  string
	delay time.Duration
	dials atomic.Int32
}

func (d *slowDialer) DialContext(ctx context.Context, network string, _ M.Socksaddr) (net.Conn, error) {
	d.dials.Add(1)
	time.Sleep(d.delay)
	var nd net.Dialer
	return nd.DialContext(ctx, network, d.addr)
}

func (d *slowDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, net.ErrClosed
}

func server(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var conns atomic.Int32
	s := httptest.NewUnstartedServer(handler)
	s.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			conns.Add(1)
		}
	}
	s.Start()
	t.Cleanup(s.Close)
	return s, &conns
}

func TestMeasureTimesARequestOnAnOpenConnection(t *testing.T) {
	var requests atomic.Int32
	s, conns := server(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodHead {
			t.Errorf("method %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	d := &slowDialer{addr: s.Listener.Addr().String(), delay: 150 * time.Millisecond}
	delay, err := Measure(context.Background(), s.URL+"/generate_204", d)
	if err != nil {
		t.Fatal(err)
	}
	if delay >= 100 {
		t.Fatalf("delay %d ms includes opening the connection", delay)
	}
	if delay == 0 {
		t.Fatal("a measured delay must not be 0, which means untested")
	}
	if d.dials.Load() != 1 || conns.Load() != 1 || requests.Load() != 2 {
		t.Fatalf("dials=%d conns=%d requests=%d, want one connection carrying two requests", d.dials.Load(), conns.Load(), requests.Load())
	}
}

func TestMeasureFallsBackWhenTheSiteClosesTheConnection(t *testing.T) {
	s, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	})
	d := &slowDialer{addr: s.Listener.Addr().String()}
	delay, err := Measure(context.Background(), s.URL, d)
	if err != nil {
		t.Fatal(err)
	}
	if delay < 20 || d.dials.Load() != 1 {
		t.Fatalf("delay=%d dials=%d, want the first request's time on the only connection", delay, d.dials.Load())
	}
}

func TestMeasureReportsAnUnreachableNode(t *testing.T) {
	d := &slowDialer{addr: "127.0.0.1:1"}
	if _, err := Measure(context.Background(), "http://example.invalid/", d); err == nil {
		t.Fatal("a node that cannot connect must fail the test")
	}
}
