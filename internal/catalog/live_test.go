package catalog_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"eggbox/internal/catalog"
	"eggbox/internal/ruledata"
	"eggbox/internal/ruleurl"
)

// These tests reach the real projects. They are opt-in because they depend on
// the network and on other people's repositories staying where they are:
//
//	EGGBOX_LIVE_RULES=1 go test -timeout 600s -run Live ./internal/catalog/
//
// What they report is addresses of public rule files, which are not secret.
func liveManager(t *testing.T, client *http.Client, mirrors []ruledata.Mirror) *ruledata.Manager {
	t.Helper()
	if os.Getenv("EGGBOX_LIVE_RULES") == "" {
		t.Skip("set EGGBOX_LIVE_RULES=1 to reach the real rule projects")
	}
	m, err := ruledata.New(ruledata.Options{Dir: t.TempDir(), Client: client, Mirrors: mirrors})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Every project in the catalog can be added: one file of each downloads, is
// recognised, and says something.
func TestLiveEveryProjectCanBeAdded(t *testing.T) {
	m := liveManager(t, &http.Client{Timeout: 90 * time.Second}, nil)
	ctx := context.Background()
	for _, p := range catalog.Projects() {
		f, err := m.Fetch(ctx, p.Example)
		if err != nil {
			t.Errorf("%s (%s): %v", p.Name, ruleurl.Display(p.Example), ruledata.Describe(err))
			continue
		}
		if f.Parsed == nil || len(f.Parsed.Rules) == 0 || f.Parsed.Stats.Domain+f.Parsed.Stats.IP == 0 {
			t.Errorf("%s: nothing in the file", p.Name)
			continue
		}
		t.Logf("%-13s %-8s read as %v: domain %d, ip %d, unsupported %d", p.Name, p.Format, f.Parsed.Format, f.Parsed.Stats.Domain, f.Parsed.Stats.IP, len(f.Parsed.Stats.Unsupported))
	}
}

// Everything the curated entries point at exists today: the recommended rules
// and every alternative.
func TestLiveCuratedEntriesAreAllReachable(t *testing.T) {
	m := liveManager(t, &http.Client{Timeout: 90 * time.Second}, nil)
	seen := map[string]bool{}
	var urls []string
	add := func(u string) {
		if !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}
	for _, e := range catalog.Entries() {
		for _, s := range e.Sources() {
			add(s.URL)
		}
		for _, a := range e.Alternatives() {
			for _, s := range a.Sources {
				add(s.URL)
			}
		}
	}
	t.Logf("%d files", len(urls))
	bad := 0
	for _, r := range m.Update(context.Background(), urls, ruledata.UpdateOptions{}) {
		if r.Outcome != ruledata.Updated {
			bad++
			t.Errorf("%s: %v", ruleurl.Display(r.URL), ruledata.Describe(r.Err))
			continue
		}
		p, err := m.Parsed(r.URL)
		if err != nil || len(p.Rules) == 0 {
			bad++
			t.Errorf("%s: unreadable or empty (%v)", ruleurl.Display(r.URL), err)
		}
	}
	if bad > 0 {
		t.Logf("%d of %d failed", bad, len(urls))
	}
}

// With GitHub unreachable, the same files come through jsDelivr.
func TestLiveJSDelivrServesWhenGitHubIsUnreachable(t *testing.T) {
	blocked := &http.Client{
		Timeout: 90 * time.Second,
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Hostname(), "githubusercontent.com") || r.URL.Hostname() == "github.com" {
				return nil, errors.New("github is not reachable from here")
			}
			return http.DefaultTransport.RoundTrip(r)
		}),
	}
	m := liveManager(t, blocked, nil)
	url := ruleurl.GeoSite("netflix")
	rs := m.Update(context.Background(), []string{url}, ruledata.UpdateOptions{})
	if len(rs) != 1 || rs[0].Outcome != ruledata.Updated {
		t.Fatalf("not served by the CDN: %+v", rs)
	}
	if !strings.Contains(strings.ToLower(rs[0].Mirror), "jsdelivr") {
		t.Errorf("served by %q, expected jsDelivr", rs[0].Mirror)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
