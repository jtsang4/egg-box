//go:build ios

// Command eggbox-tunnel is the core of EggBox's packet tunnel extension on
// iOS, built as a C archive (go build -buildmode=c-archive) that the
// extension's Swift links: native/ios/PacketTunnel, built by
// packaging/ios/build-extension.sh.
//
// The provider calls EggboxTunnelStart with the App Group container, which
// holds the configuration the application's agent accepted, and with two
// functions of its own: one applies the tunnel's network settings, the other
// ends the tunnel when the core stops by itself.
package main

/*
#include <stdlib.h>

typedef char *(*eggbox_apply_fn)(void *ctx, const char *settings);
typedef void (*eggbox_cancel_fn)(void *ctx, const char *reason);

static char *eggbox_call_apply(eggbox_apply_fn f, void *ctx, const char *s) { return f(ctx, s); }
static void eggbox_call_cancel(eggbox_cancel_fn f, void *ctx, const char *r) { f(ctx, r); }
*/
import "C"

import (
	"context"
	"errors"
	"os"
	"runtime/debug"
	"sync"
	"time"
	"unsafe"

	"eggbox/internal/phone/iostunnel"
	"eggbox/internal/phone/tunnel"
)

// The system ends a packet tunnel extension that takes more than about 50 MB.
// The collector keeps Go's heap well under it.
const memoryLimit = 34 << 20

var state struct {
	mu   sync.Mutex
	core *iostunnel.Core
	host *iostunnel.Host
	def  *tunnel.Default
}

func main() {}

// EggboxTunnelStart runs the core. It returns NULL, or the reason it could
// not start, which the caller frees.
//
//export EggboxTunnelStart
func EggboxTunnelStart(dir *C.char, apply C.eggbox_apply_fn, cancel C.eggbox_cancel_fn, ctx unsafe.Pointer) *C.char {
	debug.SetMemoryLimit(memoryLimit)
	debug.SetGCPercent(50)
	d := C.GoString(dir)
	// The socket is named from the container: its full path is too long for
	// a Unix socket's address on the simulator.
	if err := os.Chdir(d); err != nil {
		return C.CString("enter the shared container: " + err.Error())
	}
	host := &iostunnel.Host{Apply: func(settings []byte) error {
		cs := C.CString(string(settings))
		defer C.free(unsafe.Pointer(cs))
		if r := C.eggbox_call_apply(apply, ctx, cs); r != nil {
			defer C.free(unsafe.Pointer(r))
			return errors.New(C.GoString(r))
		}
		return nil
	}}
	state.mu.Lock()
	if state.def != nil {
		host.SetDefault(*state.def)
	}
	state.host = host
	state.mu.Unlock()

	sctx, scancel := context.WithTimeout(context.Background(), time.Minute)
	defer scancel()
	core, err := iostunnel.Start(sctx, d, iostunnel.SocketName, host)
	if err != nil {
		return C.CString(err.Error())
	}
	state.mu.Lock()
	state.core = core
	state.mu.Unlock()
	go func() {
		<-core.Done()
		reason := "the core stopped"
		if err := core.Exit().Err; err != nil {
			reason = err.Error()
		}
		cs := C.CString(reason)
		defer C.free(unsafe.Pointer(cs))
		C.eggbox_call_cancel(cancel, ctx, cs)
	}()
	return nil
}

// EggboxTunnelStop stops the core: the system is ending the tunnel.
//
//export EggboxTunnelStop
func EggboxTunnelStop() {
	state.mu.Lock()
	core := state.core
	state.core = nil
	state.mu.Unlock()
	if core == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = core.Stop(ctx)
}

// EggboxTunnelSetDefault tells the network the system sends traffic through
// (NWPathMonitor); index -1 is none.
//
//export EggboxTunnelSetDefault
func EggboxTunnelSetDefault(name *C.char, index C.int, expensive, constrained C.int) {
	d := tunnel.Default{Name: C.GoString(name), Index: int(index), Expensive: expensive != 0, Constrained: constrained != 0}
	state.mu.Lock()
	state.def = &d
	host := state.host
	state.mu.Unlock()
	if host != nil {
		host.SetDefault(d)
	}
}
