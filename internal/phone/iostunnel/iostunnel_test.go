package iostunnel

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/engine/remote"
	"eggbox/internal/phone/tunnel"
)

func TestThePreparedConfigurationCrossesTheContainer(t *testing.T) {
	dir := t.TempDir()
	in := engine.Prepared{Revision: 7, JSON: []byte(`{"log":{"level":"info"}}`), HasTun: true, TunTag: "tun-in", LogLevel: "info"}
	if err := WritePrepared(dir, in); err != nil {
		t.Fatal(err)
	}
	out, err := ReadPrepared(dir)
	if err != nil {
		t.Fatal(err)
	}
	if out.Revision != 7 || !out.HasTun || out.TunTag != "tun-in" || string(out.JSON) != string(in.JSON) {
		t.Fatalf("read %+v", out)
	}
	if err := WritePrepared(dir, engine.Prepared{JSON: []byte("not json")}); err == nil {
		t.Fatal("a configuration that is not JSON was handed over")
	}
}

func TestSettingsAreWhatTheProviderTakes(t *testing.T) {
	b, err := SettingsJSON(tunnel.Settings{
		Addresses: []netip.Prefix{netip.MustParsePrefix("172.19.0.1/30"), netip.MustParsePrefix("fdfe:dcba:9876::1/126")},
		Routes:    []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")},
		DNS:       []netip.Addr{netip.MustParseAddr("172.19.0.2")},
	})
	if err != nil {
		t.Fatal(err)
	}
	var s Settings
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Addresses) != 2 || s.Addresses[0].Mask != "255.255.255.252" || s.Addresses[0].IPv6 || !s.Addresses[1].IPv6 || s.Addresses[1].Bits != 126 {
		t.Fatalf("addresses %+v", s.Addresses)
	}
	if len(s.Routes) != 2 || s.Routes[0].Bits != 0 || s.MTU != 1500 || len(s.DNS) != 1 {
		t.Fatalf("settings %+v", s)
	}
}

func TestTheDefaultNetworkReachesTheCore(t *testing.T) {
	h := &Host{}
	h.SetDefault(tunnel.Default{Name: "en0", Index: 12})
	var got []string
	stop, err := h.WatchDefault(func(d tunnel.Default) { got = append(got, d.Name) })
	if err != nil {
		t.Fatal(err)
	}
	h.SetDefault(tunnel.Default{Name: "pdp_ip0", Index: 3, Expensive: true})
	stop()
	h.SetDefault(tunnel.Default{Name: "en0", Index: 12})
	if len(got) != 2 || got[0] != "en0" || got[1] != "pdp_ip0" {
		t.Fatalf("watched %v", got)
	}
	if !h.UnderNetworkExtension() || h.Protect(3) != nil {
		t.Fatal("the extension's sockets need no protection")
	}
}

func TestOpenTunAppliesTheSettingsThenFindsTheInterface(t *testing.T) {
	var applied []byte
	h := &Host{
		Apply:   func(b []byte) error { applied = b; return nil },
		FindTun: func() (int, error) { return 42, nil },
	}
	fd, err := h.OpenTun(tunnel.Settings{Addresses: []netip.Prefix{netip.MustParsePrefix("172.19.0.1/30")}, MTU: 9000})
	if err != nil || fd != 42 || len(applied) == 0 {
		t.Fatalf("fd %d, err %v, applied %s", fd, err, applied)
	}
}

// TestTheExtensionServesTheRealCore runs what the extension runs, without a
// TUN (a Mac makes no utun for a test): the configuration from the
// container, the core, and the socket the application reaches it on.
func TestTheExtensionServesTheRealCore(t *testing.T) {
	dir, err := os.MkdirTemp("", "ext")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	cfg := map[string]any{
		"log":      map[string]any{"level": "info"},
		"inbounds": []any{map[string]any{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 0}},
		"outbounds": []any{
			map[string]any{"type": "direct", "tag": "direct"},
		},
		"experimental": map[string]any{"cache_file": map[string]any{"enabled": true, "path": filepath.Join(dir, "cache.db")}},
	}
	raw, _ := json.Marshal(cfg)
	if err := WritePrepared(dir, engine.Prepared{Revision: 1, JSON: raw}); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, SocketName)
	host := &Host{}
	host.SetDefault(tunnel.Default{Name: "en0", Index: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	core, err := Start(ctx, dir, sock, host)
	if err != nil {
		t.Fatal(err)
	}
	c := &remote.Client{Dial: func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}
	st, err := c.Status(ctx)
	if err != nil || !st.Running || st.Version == "" {
		t.Fatalf("status %+v, %v", st, err)
	}
	if err := core.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-core.Done():
		t.Fatal("a stop that was asked for is not an exit")
	default:
	}
}
