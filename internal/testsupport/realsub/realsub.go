// Package realsub gives tests the developer's real subscription, when there is
// one, and keeps what is in it out of every place a test could print to.
//
// The link and the nodes behind it are credentials. They live outside the
// repository: EGGBOX_TEST_SUBSCRIPTION_FILE points at a private file, or
// EGGBOX_TEST_SUBSCRIPTION_URL carries the link. A test that uses them logs
// counts and protocol names only, never an address, a name, a key, and never
// an error message before it went through Scrub.
package realsub

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
)

// URL returns the real subscription's link, or skips the test.
func URL(t testing.TB) string {
	t.Helper()
	if u := strings.TrimSpace(os.Getenv("EGGBOX_TEST_SUBSCRIPTION_URL")); u != "" {
		return u
	}
	if f := os.Getenv("EGGBOX_TEST_SUBSCRIPTION_FILE"); f != "" {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("cannot read EGGBOX_TEST_SUBSCRIPTION_FILE: %v", err)
		}
		if u := strings.TrimSpace(string(raw)); u != "" {
			return u
		}
	}
	t.Skip("set EGGBOX_TEST_SUBSCRIPTION_FILE (or EGGBOX_TEST_SUBSCRIPTION_URL) to run against a real subscription")
	return ""
}

// Scrub removes the subscription link, and the pieces of it that identify it,
// from text that is about to be printed.
func Scrub(link, s string) string {
	if link == "" {
		return s
	}
	s = strings.ReplaceAll(s, link, "<subscription>")
	if u, err := url.Parse(link); err == nil {
		for _, part := range []string{u.RawQuery, strings.Trim(u.Path, "/"), u.Host, u.Hostname()} {
			if len(part) >= 4 {
				s = strings.ReplaceAll(s, part, "<subscription>")
			}
		}
	}
	return s
}

// Class names what kind of failure err is, in words that carry no address:
// the text of a network error holds the server it was talking to.
func Class(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	msg := strings.ToLower(err.Error())
	for _, k := range []struct{ needle, class string }{
		{"timeout", "timeout"},
		{"deadline", "timeout"},
		{"refused", "refused"},
		{"reset", "reset"},
		{"eof", "closed"},
		{"tls", "tls"},
		{"certificate", "tls"},
		{"no such host", "dns"},
		{"lookup", "dns"},
		{"unreachable", "unreachable"},
	} {
		if strings.Contains(msg, k.needle) {
			return k.class
		}
	}
	return "other"
}
