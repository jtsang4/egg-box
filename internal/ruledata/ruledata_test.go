package ruledata_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"eggbox/internal/ruledata"
	"eggbox/internal/testsupport/srs"
)

// mirrorOf serves files under /<repo>/<file> and counts the requests.
func mirrorOf(t *testing.T, name string, files map[string][]byte, hits *atomic.Int32) ruledata.Mirror {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		data, ok := files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		const tag = `"v1"`
		if r.Header.Get("If-None-Match") == tag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", tag)
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return ruledata.Mirror{Name: name, URL: func(repo, file string) string { return srv.URL + "/" + repo + "/" + file }}
}

func TestUpdateStoresAndLoads(t *testing.T) {
	cn := srs.DomainSuffixes(t, "example.cn")
	ip := srs.CIDRs(t, "10.0.0.0/8")
	mirror := mirrorOf(t, "test", map[string][]byte{
		"sing-geosite/geosite-cn.srs": cn,
		"sing-geoip/geoip-cn.srs":     ip,
	}, nil)
	dir := t.TempDir()
	m, err := ruledata.New(ruledata.Options{Dir: dir, Mirrors: []ruledata.Mirror{mirror}})
	if err != nil {
		t.Fatal(err)
	}

	res := m.Update(context.Background(), []string{"geosite-cn", "geoip-cn", "geosite-missing"}, ruledata.UpdateOptions{})
	if res[0].Outcome != ruledata.Updated || res[1].Outcome != ruledata.Updated || res[0].Source != "test" {
		t.Fatalf("results %+v", res)
	}
	if res[2].Outcome != ruledata.Failed || res[2].Err == nil {
		t.Fatalf("an unknown rule set must fail: %+v", res[2])
	}

	sets, missing := m.Load("geosite-cn", "geoip-cn", "geosite-missing")
	if string(sets["geosite-cn"]) != string(cn) || string(sets["geoip-cn"]) != string(ip) {
		t.Error("the stored files differ from what the mirror served")
	}
	if len(missing) != 1 || missing[0] != "geosite-missing" {
		t.Errorf("missing = %v", missing)
	}
	if info, err := os.Stat(filepath.Join(dir, "geosite-cn.srs")); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Errorf("stored file: %v, %v", info, err)
	}

	// A new manager over the same directory knows what is there.
	m2, err := ruledata.New(ruledata.Options{Dir: dir, Mirrors: []ruledata.Mirror{mirror}})
	if err != nil {
		t.Fatal(err)
	}
	st := m2.Statuses("geosite-cn", "geosite-missing")
	if !st[0].Present || st[0].Meta.Size != int64(len(cn)) || st[0].Meta.Source != "test" || st[1].Present {
		t.Errorf("statuses %+v", st)
	}
}

func TestConditionalRequestsAndMaxAge(t *testing.T) {
	data := srs.DomainSuffixes(t, "example.cn")
	var hits atomic.Int32
	mirror := mirrorOf(t, "test", map[string][]byte{"sing-geosite/geosite-cn.srs": data}, &hits)
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	now := start
	m, _ := ruledata.New(ruledata.Options{Dir: t.TempDir(), Mirrors: []ruledata.Mirror{mirror}, Now: func() time.Time { return now }})

	m.Update(context.Background(), []string{"geosite-cn"}, ruledata.UpdateOptions{})
	if hits.Load() != 1 {
		t.Fatalf("hits = %d", hits.Load())
	}

	// Recent enough: no request at all.
	now = start.Add(time.Hour)
	if r := m.Update(context.Background(), []string{"geosite-cn"}, ruledata.UpdateOptions{MaxAge: 24 * time.Hour}); r[0].Outcome != ruledata.Skipped || hits.Load() != 1 {
		t.Errorf("expected a skip without a request: %+v, hits %d", r[0], hits.Load())
	}

	// Stale: a conditional request that finds the copy current.
	now = start.Add(48 * time.Hour)
	if r := m.Update(context.Background(), []string{"geosite-cn"}, ruledata.UpdateOptions{MaxAge: 24 * time.Hour}); r[0].Outcome != ruledata.Unchanged || hits.Load() != 2 {
		t.Errorf("expected Unchanged: %+v, hits %d", r[0], hits.Load())
	}
	if st := m.Statuses("geosite-cn")[0]; !st.Meta.CheckedAt.Equal(now) || !st.Meta.UpdatedAt.Equal(start) {
		t.Errorf("a confirmed copy is checked, not updated: %+v", st.Meta)
	}

	// Forced: downloads again whatever the mirror says.
	if r := m.Update(context.Background(), []string{"geosite-cn"}, ruledata.UpdateOptions{Force: true}); r[0].Outcome != ruledata.Updated {
		t.Errorf("expected Updated: %+v", r[0])
	}
}

func TestFallsBackToTheNextMirror(t *testing.T) {
	data := srs.DomainSuffixes(t, "example.cn")
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) }))
	t.Cleanup(broken.Close)
	bad := ruledata.Mirror{Name: "broken", URL: func(repo, file string) string { return broken.URL + "/" + repo + "/" + file }}
	good := mirrorOf(t, "good", map[string][]byte{"sing-geosite/geosite-cn.srs": data}, nil)

	m, _ := ruledata.New(ruledata.Options{Dir: t.TempDir(), Mirrors: []ruledata.Mirror{bad, good}})
	r := m.Update(context.Background(), []string{"geosite-cn"}, ruledata.UpdateOptions{})
	if r[0].Outcome != ruledata.Updated || r[0].Source != "good" {
		t.Fatalf("%+v", r[0])
	}

	// With every mirror failing the error names them and nothing is stored.
	m2, _ := ruledata.New(ruledata.Options{Dir: t.TempDir(), Mirrors: []ruledata.Mirror{bad}})
	r = m2.Update(context.Background(), []string{"geosite-cn"}, ruledata.UpdateOptions{})
	if r[0].Outcome != ruledata.Failed || !strings.Contains(r[0].Err.Error(), "broken") {
		t.Fatalf("%+v", r[0])
	}
	if _, missing := m2.Load("geosite-cn"); len(missing) != 1 {
		t.Error("a failed download left a file behind")
	}
}

func TestRejectsGarbageAndKeepsTheOldCopy(t *testing.T) {
	good := srs.DomainSuffixes(t, "example.cn")
	files := map[string][]byte{"sing-geosite/geosite-cn.srs": good}
	var hits atomic.Int32
	mirror := mirrorOf(t, "test", files, &hits)
	m, _ := ruledata.New(ruledata.Options{Dir: t.TempDir(), Mirrors: []ruledata.Mirror{mirror}})
	m.Update(context.Background(), []string{"geosite-cn"}, ruledata.UpdateOptions{})

	files["sing-geosite/geosite-cn.srs"] = []byte("<html>blocked by a captive portal</html>")
	r := m.Update(context.Background(), []string{"geosite-cn"}, ruledata.UpdateOptions{Force: true})
	if r[0].Outcome != ruledata.Failed {
		t.Fatalf("garbage was accepted: %+v", r[0])
	}
	sets, _ := m.Load("geosite-cn")
	if string(sets["geosite-cn"]) != string(good) {
		t.Error("the good copy was replaced")
	}
}

func TestUnsafeIDsAreRefused(t *testing.T) {
	m, _ := ruledata.New(ruledata.Options{Dir: t.TempDir(), Mirrors: []ruledata.Mirror{{Name: "x", URL: func(string, string) string { return "http://127.0.0.1:1/never" }}}})
	for _, id := range []string{"", "../etc/passwd", "geosite-../x", "geosite-a/b", "other-cn", "geosite-" + strings.Repeat("a", 200)} {
		if r := m.Update(context.Background(), []string{id}, ruledata.UpdateOptions{}); r[0].Outcome != ruledata.Failed {
			t.Errorf("id %q was accepted", id)
		}
	}
	if sets, missing := m.Load("../etc/passwd"); len(sets) != 0 || len(missing) != 1 {
		t.Error("load must not read outside the directory")
	}
}

func TestSpecialCharactersInIDs(t *testing.T) {
	data := srs.DomainSuffixes(t, "example.org")
	var requested atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested.Store(r.URL.Path)
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	mirror := ruledata.Mirror{Name: "t", URL: func(repo, file string) string { return srv.URL + "/" + repo + "/" + file }}
	m, _ := ruledata.New(ruledata.Options{Dir: t.TempDir(), Mirrors: []ruledata.Mirror{mirror}})
	if r := m.Update(context.Background(), []string{"geosite-geolocation-!cn"}, ruledata.UpdateOptions{}); r[0].Outcome != ruledata.Updated {
		t.Fatalf("%+v", r[0])
	}
	if got := requested.Load().(string); !strings.HasSuffix(got, "/geosite-geolocation-!cn.srs") {
		t.Errorf("requested %q", got)
	}
	if sets, _ := m.Load("geosite-geolocation-!cn"); len(sets) != 1 {
		t.Error("the rule set was not stored under a safe name")
	}
}

func TestBuiltInMirrorsPointAtTheRuleSetBranch(t *testing.T) {
	for _, mir := range ruledata.DefaultMirrors() {
		got := mir.URL("sing-geosite", "geosite-cn.srs")
		if !strings.Contains(got, "SagerNet/sing-geosite") || !strings.Contains(got, "rule-set") || !strings.HasSuffix(got, "geosite-cn.srs") {
			t.Errorf("%s: %s", mir.Name, got)
		}
	}
	if _, ok := ruledata.MirrorByName("jsdelivr"); !ok {
		t.Error("jsdelivr is not found by name")
	}
	if _, ok := ruledata.MirrorByName("nope"); ok {
		t.Error("an unknown mirror was found")
	}
}
