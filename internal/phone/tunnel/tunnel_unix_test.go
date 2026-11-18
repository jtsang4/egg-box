//go:build unix

package tunnel

import (
	"net/netip"
	"os"
	"testing"

	"github.com/sagernet/sing-box/option"
	tun "github.com/sagernet/sing-tun"
)

// The core's duplicate of the system's descriptor is a Unix one: phones are.

func TestOpenInterfaceUsesTheSystemsInterface(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	h := &fakeHost{fd: int(r.Fd())}
	p := New(h)
	p.tunName = func(fd int) (string, error) {
		if fd != h.fd {
			t.Errorf("name read from fd %d, want the system's %d", fd, h.fd)
		}
		return "tun0", nil
	}
	var got tun.Options
	p.newTun = func(o tun.Options) (tun.Tun, error) { got = o; return nil, nil }

	o := &tun.Options{Inet4Address: prefixes("172.19.0.1/30"), MTU: 1500, AutoRoute: true}
	if _, err := p.OpenInterface(o, option.TunPlatformOptions{}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.NewFile(uintptr(got.FileDescriptor), "dup").Close() }()
	if len(h.opened) != 1 || h.opened[0].MTU != 1500 {
		t.Fatalf("opened %+v", h.opened)
	}
	if got.Name != "tun0" || o.Name != "tun0" || p.TunName() != "tun0" {
		t.Fatalf("name: options %q, given %q, platform %q", got.Name, o.Name, p.TunName())
	}
	// The core works on a duplicate: closing it leaves the system's open.
	if got.FileDescriptor == h.fd || got.FileDescriptor <= 0 {
		t.Fatalf("the core got fd %d, the system's is %d", got.FileDescriptor, h.fd)
	}
	if addrs := p.MyInterfaceAddress(); len(addrs) != 1 || addrs[0] != netip.MustParseAddr("172.19.0.1") {
		t.Fatalf("my addresses %v", addrs)
	}

	p.CloseTun()
	if h.closed != 1 || p.TunName() != "" || len(p.MyInterfaceAddress()) != 0 {
		t.Fatalf("after CloseTun: host closed %d, name %q", h.closed, p.TunName())
	}
}
