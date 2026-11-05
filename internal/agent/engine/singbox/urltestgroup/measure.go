// Package urltestgroup is sing-box's "urltest" outbound group with one
// difference: the delay of a node is the round trip of a request on a
// connection through it that is already open, not the time it takes to open
// one.
//
// sing-box times a new connection: the TCP and TLS (Reality, ...) handshakes
// with the node, the TLS handshake with the test site through it, and then the
// request, which is four or five round trips. Other clients (mihomo's "unified
// delay", Shadowrocket) show the round trip itself, so the same node looked
// several times slower here than there. The ranking of nodes does not change.
//
// Adapted from sing-box v1.14.4 (protocol/group/urltest.go and
// common/urltest/urltest.go), Copyright (C) 2022 by nekohasekai, under the GNU
// General Public License version 3 or later.
package urltestgroup

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"
)

// DefaultLink is the page measured when a group names none.
const DefaultLink = "https://www.gstatic.com/generate_204"

var errOneConnection = errors.New("the delay test uses a single connection")

// Measure returns the round trip of a HEAD request for link through detour,
// in milliseconds. The first request opens the connection and pays for every
// handshake; the second one, on the same connection, is timed. When the site
// closes the connection after the first answer, the first request's time is
// the result.
func Measure(ctx context.Context, link string, detour N.Dialer) (uint16, error) {
	if link == "" {
		link = DefaultLink
	}
	u, err := url.Parse(link)
	if err != nil {
		return 0, err
	}
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	instance, err := detour.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddrHostPortStr(u.Hostname(), port))
	if err != nil {
		return 0, err
	}
	defer instance.Close()
	var dialed atomic.Bool
	client := http.Client{
		Transport: &http.Transport{
			DialContext: func(context.Context, string, string) (net.Conn, error) {
				if dialed.Swap(true) {
					return nil, errOneConnection
				}
				return instance, nil
			},
			TLSClientConfig: &tls.Config{
				Time:    ntp.TimeFuncFromContext(ctx),
				RootCAs: adapter.RootPoolFromContext(ctx),
			},
			MaxIdleConnsPerHost: 1,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       C.TCPTimeout,
	}
	defer client.CloseIdleConnections()
	head := func() (time.Duration, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, link, nil)
		if err != nil {
			return 0, err
		}
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return time.Since(start), nil
	}
	first, err := head()
	if err != nil {
		return 0, err
	}
	if second, err := head(); err == nil {
		return millis(second), nil
	}
	return millis(first), nil
}

// millis is d in whole milliseconds, at least 1: a delay of 0 means "not
// measured" to the group.
func millis(d time.Duration) uint16 {
	ms := d.Milliseconds()
	switch {
	case ms < 1:
		return 1
	case ms > 65535:
		return 65535
	}
	return uint16(ms)
}
