package ruledata_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"eggbox/internal/ruledata"
	"eggbox/internal/rulefmt"
	"eggbox/internal/ruleurl"
)

const (
	raw     = "https://raw.githubusercontent.com/"
	listURL = raw + "o/r/main/rules/list.txt"
	jsonURL = raw + "o/r/main/rules/set.json"
	srsURL  = raw + "o/r/main/rules/set.srs"
)

// network is an invented internet: handlers per host and path, a count of
// requests, and a way to take a host down.
type network struct {
	mu    sync.Mutex
	files map[string][]byte // "host/path" -> body
	etags map[string]string
	down  map[string]bool // hosts that do not answer
	hits  map[string]int
	seen  []string // every request URL, for leak checks
}

func newNetwork() *network {
	return &network{files: map[string][]byte{}, etags: map[string]string{}, down: map[string]bool{}, hits: map[string]int{}}
}

func (n *network) put(url string, body []byte) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.files[strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")] = body
}

func (n *network) hitsOf(url string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.hits[strings.TrimPrefix(url, "https://")]
}

func (n *network) RoundTrip(req *http.Request) (*http.Response, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.seen = append(n.seen, req.URL.String())
	if n.down[req.URL.Hostname()] {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("i/o timeout")}
	}
	key := req.URL.Host + req.URL.Path
	n.hits[key]++
	body, ok := n.files[key]
	resp := &http.Response{Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{}, Request: req}
	if !ok {
		resp.StatusCode, resp.Body = http.StatusNotFound, io.NopCloser(strings.NewReader("not found"))
		return resp, nil
	}
	if tag := n.etags[key]; tag != "" {
		resp.Header.Set("ETag", tag)
		if req.Header.Get("If-None-Match") == tag {
			resp.StatusCode, resp.Body = http.StatusNotModified, io.NopCloser(strings.NewReader(""))
			return resp, nil
		}
	}
	resp.StatusCode, resp.Body, resp.ContentLength = http.StatusOK, io.NopCloser(bytes.NewReader(body)), int64(len(body))
	return resp, nil
}

func (n *network) client() *http.Client { return &http.Client{Transport: n, Timeout: 20 * time.Second} }

func jsonRules(domains ...string) []byte {
	q := make([]string, len(domains))
	for i, d := range domains {
		q[i] = `"` + d + `"`
	}
	return []byte(`{"version":3,"rules":[{"domain_suffix":[` + strings.Join(q, ",") + `]}]}`)
}

func srsOf(t *testing.T, domains ...string) []byte {
	t.Helper()
	p, err := rulefmt.Parse(jsonRules(domains...), rulefmt.Hint{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := rulefmt.Compile(p.Rules)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func open(t *testing.T, n *network, mirrors ...ruledata.Mirror) *ruledata.Manager {
	t.Helper()
	m, err := ruledata.New(ruledata.Options{Dir: t.TempDir(), Client: n.client(), Mirrors: mirrors})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestUpdateStoresReadsAndRemembersFiles(t *testing.T) {
	n := newNetwork()
	n.put(listURL, []byte("DOMAIN-SUFFIX,example.org\nIP-CIDR,203.0.113.0/24\n# a comment\n"))
	n.put(jsonURL, jsonRules("example.net"))
	n.put(srsURL, srsOf(t, "example.com"))
	dir := t.TempDir()
	m, err := ruledata.New(ruledata.Options{Dir: dir, Client: n.client(), Mirrors: []ruledata.Mirror{ruledata.GitHub}})
	if err != nil {
		t.Fatal(err)
	}
	res := m.Update(context.Background(), []string{listURL, jsonURL, srsURL, raw + "o/r/main/missing.txt"}, ruledata.UpdateOptions{})
	for i, want := range []ruledata.Outcome{ruledata.Updated, ruledata.Updated, ruledata.Updated, ruledata.Failed} {
		if res[i].Outcome != want {
			t.Errorf("result %d = %+v, want outcome %v", i, res[i], want)
		}
	}
	var se ruledata.StatusError
	if !errors.As(res[3].Err, &se) || int(se) != 404 {
		t.Errorf("a missing file is an HTTP 404: %v", res[3].Err)
	}
	for url, wantFormat := range map[string]rulefmt.Format{listURL: rulefmt.FormatClassical, jsonURL: rulefmt.FormatSingBox, srsURL: rulefmt.FormatSRS} {
		p, err := m.Parsed(url)
		if err != nil || p.Format != wantFormat {
			t.Errorf("Parsed(%s) = %v, %v; want %s", url, p, err, wantFormat)
		}
		meta, ok := m.Meta(url)
		if !ok || meta.Size == 0 || meta.SHA256 == "" || meta.UpdatedAt.IsZero() || meta.URL != url || meta.Format != string(wantFormat) {
			t.Errorf("Meta(%s) = %+v", url, meta)
		}
	}
	meta, _ := m.Meta(listURL)
	if meta.Stats.Domain != 1 || meta.Stats.IP != 1 {
		t.Errorf("counts are kept with the file: %+v", meta.Stats)
	}
	if data, err := m.Data(listURL); err != nil || !bytes.Contains(data, []byte("example.org")) {
		t.Errorf("the file is kept as it arrived: %q %v", data, err)
	}
	if _, err := m.Data(raw + "o/r/main/missing.txt"); !errors.Is(err, ruledata.ErrNotStored) {
		t.Errorf("a file that failed is not stored: %v", err)
	}

	// Everything survives a restart.
	again, err := ruledata.New(ruledata.Options{Dir: dir, Client: n.client(), Mirrors: []ruledata.Mirror{ruledata.GitHub}})
	if err != nil {
		t.Fatal(err)
	}
	if p, err := again.Parsed(jsonURL); err != nil || len(p.Rules) == 0 {
		t.Errorf("after a restart: %v %v", p, err)
	}
	sts := again.Statuses()
	if len(sts) != 3 {
		t.Fatalf("Statuses = %d entries, want the 3 stored files", len(sts))
	}
	for _, st := range sts {
		if !st.Present {
			t.Errorf("%s should be present", st.URL)
		}
	}
	// Nothing about the files lives outside the directory handed over.
	if _, err := os.Stat(filepath.Join(dir, "index.json")); err != nil {
		t.Errorf("index.json: %v", err)
	}
}

func TestParsedIsKeptUntilTheFileChanges(t *testing.T) {
	n := newNetwork()
	n.put(jsonURL, jsonRules("example.net"))
	m := open(t, n, ruledata.GitHub)
	m.Update(context.Background(), []string{jsonURL}, ruledata.UpdateOptions{})
	a, _ := m.Parsed(jsonURL)
	b, _ := m.Parsed(jsonURL)
	if a != b {
		t.Error("the same file must not be read twice")
	}
	n.put(jsonURL, jsonRules("example.net", "example.org"))
	if r := m.Update(context.Background(), []string{jsonURL}, ruledata.UpdateOptions{Force: true}); r[0].Outcome != ruledata.Updated {
		t.Fatalf("changed content: %+v", r[0])
	}
	c, _ := m.Parsed(jsonURL)
	if c == a || rulefmt.Count(c.Rules).Domain != 2 {
		t.Errorf("a changed file is read again: %+v", rulefmt.Count(c.Rules))
	}
}

func TestSchedulingAndConditionalRequests(t *testing.T) {
	n := newNetwork()
	n.put(jsonURL, jsonRules("example.net"))
	n.etags["raw.githubusercontent.com/o/r/main/rules/set.json"] = `"v1"`
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := ruledata.New(ruledata.Options{Dir: t.TempDir(), Client: n.client(), Mirrors: []ruledata.Mirror{ruledata.GitHub}, Now: func() time.Time { return now }})
	ctx := context.Background()
	day := 24 * time.Hour

	m.Update(ctx, []string{jsonURL}, ruledata.UpdateOptions{})
	if h := n.hitsOf(jsonURL); h != 1 {
		t.Fatalf("hits = %d", h)
	}
	now = now.Add(time.Hour)
	if r := m.Update(ctx, []string{jsonURL}, ruledata.UpdateOptions{MaxAge: day}); r[0].Outcome != ruledata.Skipped || n.hitsOf(jsonURL) != 1 {
		t.Errorf("a recently checked file is skipped without a request: %+v hits=%d", r[0], n.hitsOf(jsonURL))
	}
	now = now.Add(day)
	r := m.Update(ctx, []string{jsonURL}, ruledata.UpdateOptions{MaxAge: day})
	if r[0].Outcome != ruledata.Unchanged || n.hitsOf(jsonURL) != 2 {
		t.Errorf("an old check asks again and the server says 304: %+v", r[0])
	}
	if meta, _ := m.Meta(jsonURL); !meta.CheckedAt.Equal(now) || meta.UpdatedAt.Equal(now) {
		t.Errorf("checking moves CheckedAt but not UpdatedAt: %+v", meta)
	}
	if r := m.Update(ctx, []string{jsonURL}, ruledata.UpdateOptions{Force: true}); r[0].Outcome != ruledata.Unchanged {
		t.Errorf("forcing a download of the same bytes changes nothing: %+v", r[0])
	}

	// A server without ETags: identical bytes are recognised by their hash.
	plain := raw + "o/r/main/plain.json"
	n.put(plain, jsonRules("example.org"))
	m.Update(ctx, []string{plain}, ruledata.UpdateOptions{})
	if r := m.Update(ctx, []string{plain}, ruledata.UpdateOptions{Force: true}); r[0].Outcome != ruledata.Unchanged {
		t.Errorf("same bytes, no etag: %+v", r[0])
	}
}

func TestFallsBackThroughMirrorsAndNamesTheOneThatWorked(t *testing.T) {
	n := newNetwork()
	file := srsOf(t, "example.net")
	n.put(srsURL, file)
	n.put(ruleurl.JSDelivr(srsURL), file)
	n.put("proxy.example/"+srsURL, file)
	m := open(t, n, ruledata.DefaultMirrors("https://proxy.example/")...)

	n.down["raw.githubusercontent.com"] = true
	r := m.Update(context.Background(), []string{srsURL}, ruledata.UpdateOptions{})
	if r[0].Outcome != ruledata.Updated || r[0].Mirror != "jsdelivr" {
		t.Fatalf("GitHub down: %+v", r[0])
	}

	n.down["cdn.jsdelivr.net"] = true
	r = m.Update(context.Background(), []string{srsURL}, ruledata.UpdateOptions{Force: true})
	if r[0].Outcome != ruledata.Unchanged || r[0].Mirror != "custom" {
		t.Fatalf("GitHub and the CDN down: %+v", r[0])
	}

	n.down["proxy.example"] = true
	r = m.Update(context.Background(), []string{srsURL}, ruledata.UpdateOptions{Force: true})
	if r[0].Outcome != ruledata.Failed || r[0].Err == nil {
		t.Fatalf("everything down: %+v", r[0])
	}
	if !strings.Contains(r[0].Err.Error(), "i/o timeout") {
		t.Errorf("the failure says what happened: %v", r[0].Err)
	}
	if _, err := m.Parsed(srsURL); err != nil {
		t.Errorf("the copy already stored keeps working: %v", err)
	}
	if meta, _ := m.Meta(srsURL); meta.LastError == "" {
		t.Error("the reason is remembered next to the stored copy")
	}
}

func TestGitHubUnreachableUsesJSDelivrOnlyForGitHubFiles(t *testing.T) {
	n := newNetwork()
	n.down["raw.githubusercontent.com"] = true
	n.put(ruleurl.JSDelivr(srsURL), srsOf(t, "example.net"))
	other := "https://rules.example/x.json"
	n.put(other, jsonRules("example.org"))
	m := open(t, n, ruledata.DefaultMirrors("")...)
	r := m.Update(context.Background(), []string{srsURL, other}, ruledata.UpdateOptions{})
	if r[0].Outcome != ruledata.Updated || r[0].Mirror != "jsdelivr" {
		t.Errorf("a GitHub file comes through the CDN: %+v", r[0])
	}
	if r[1].Outcome != ruledata.Updated || r[1].Mirror != "github" {
		t.Errorf("another site's file is fetched from where it lives: %+v", r[1])
	}
	for _, u := range n.seen {
		if strings.Contains(u, "jsdelivr") && strings.Contains(u, "rules.example") {
			t.Errorf("a CDN was asked for a file that is not on GitHub: %s", u)
		}
	}
}

func TestAMirrorThatAnswersWithAWebPageIsNotBelieved(t *testing.T) {
	n := newNetwork()
	n.put(jsonURL, []byte("<!DOCTYPE html><html><body>Please sign in</body></html>"))
	good := ruleurl.JSDelivr(jsonURL)
	n.put(good, jsonRules("example.net"))
	m := open(t, n, ruledata.GitHub, ruledata.JSDelivr)
	r := m.Update(context.Background(), []string{jsonURL}, ruledata.UpdateOptions{})
	if r[0].Outcome != ruledata.Updated || r[0].Mirror != "jsdelivr" {
		t.Fatalf("%+v", r[0])
	}
	// And a later page in place of the file does not replace what is stored.
	n.put(good, []byte("<html>oops</html>"))
	r = m.Update(context.Background(), []string{jsonURL}, ruledata.UpdateOptions{Force: true})
	if r[0].Outcome != ruledata.Failed || !errors.Is(r[0].Err, rulefmt.ErrUnrecognized) {
		t.Errorf("garbage is a failure that names the problem: %+v", r[0])
	}
	if p, err := m.Parsed(jsonURL); err != nil || rulefmt.Count(p.Rules).Domain != 1 {
		t.Errorf("the previous copy stays: %v %v", p, err)
	}
}

func TestLimits(t *testing.T) {
	n := newNetwork()
	n.put(jsonURL, bytes.Repeat([]byte("DOMAIN,example.net\n"), (ruledata.MaxSize/19)+10))
	m := open(t, n, ruledata.GitHub)
	r := m.Update(context.Background(), []string{jsonURL}, ruledata.UpdateOptions{})
	if r[0].Outcome != ruledata.Failed || !errors.Is(r[0].Err, ruledata.ErrTooLarge) {
		t.Errorf("an oversized file is refused: %+v", r[0])
	}
	if _, err := m.Data(jsonURL); err == nil {
		t.Error("an oversized file is not stored")
	}

	// A server that does not say how long the body is is cut off as well.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		line := []byte("DOMAIN,example.net\n")
		for i := 0; i < ruledata.MaxSize/len(line)+10; i++ {
			if _, err := w.Write(line); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	live, _ := ruledata.New(ruledata.Options{Dir: t.TempDir(), Mirrors: []ruledata.Mirror{{Name: "t", URL: func(string) string { return srv.URL + "/big" }}}})
	r = live.Update(context.Background(), []string{jsonURL}, ruledata.UpdateOptions{})
	if r[0].Outcome != ruledata.Failed || !errors.Is(r[0].Err, ruledata.ErrTooLarge) {
		t.Errorf("a body of unknown length is limited too: %+v", r[0])
	}
}

func TestMihomoBinaryFilesAreNamedAsUnreadable(t *testing.T) {
	n := newNetwork()
	mrs := "https://rules.example/x.mrs"
	n.put(mrs, append([]byte("MRS\x01"), bytes.Repeat([]byte{0}, 64)...))
	m := open(t, n, ruledata.GitHub)
	f, err := m.Fetch(context.Background(), mrs)
	if f != nil || !errors.Is(err, rulefmt.ErrMRS) {
		t.Fatalf("Fetch = %v, %v; want ErrMRS", f, err)
	}
	r := m.Update(context.Background(), []string{mrs}, ruledata.UpdateOptions{})
	if r[0].Outcome != ruledata.Failed || !errors.Is(r[0].Err, rulefmt.ErrMRS) {
		t.Errorf("Update: %+v", r[0])
	}

	// A known project's .mrs is read as the project's sing-box file instead.
	meta := raw + "MetaCubeX/meta-rules-dat/meta/geo/geosite/openai.mrs"
	n.put(raw+"MetaCubeX/meta-rules-dat/sing/geo/geosite/openai.srs", srsOf(t, "example.net"))
	got, err := m.Fetch(context.Background(), meta)
	if err != nil {
		t.Fatalf("Fetch(mapped .mrs): %v", err)
	}
	if got.Info.URL != ruleurl.GeoSite("openai") || got.Parsed.Format != rulefmt.FormatSRS || !got.Info.Swapped || got.Info.Converted {
		t.Errorf("mapped fetch = %+v", got.Info)
	}
}

func TestFetchShowsWhatAFileHoldsAndKeepStoresIt(t *testing.T) {
	n := newNetwork()
	n.put(listURL, []byte("DOMAIN-SUFFIX,example.org\nDOMAIN-SUFFIX,example.net\nUSER-AGENT,foo\nIP-CIDR,203.0.113.0/24,no-resolve\n"))
	m := open(t, n, ruledata.GitHub)
	f, err := m.Fetch(context.Background(), "https://gh-proxy.example/"+listURL)
	if err != nil {
		t.Fatal(err)
	}
	if f.Info.URL != listURL || !f.Info.Stripped {
		t.Errorf("the pasted address is cleaned up: %+v", f.Info)
	}
	st := f.Parsed.Stats
	if st.Domain != 2 || st.IP != 1 || st.UnsupportedTotal() != 1 {
		t.Errorf("preview counts = %+v", st)
	}
	if _, err := m.Data(listURL); err == nil {
		t.Fatal("Fetch alone must not store anything")
	}
	if err := m.Keep(f); err != nil {
		t.Fatal(err)
	}
	if p, err := m.Parsed(listURL); err != nil || p.Stats.Domain != 2 {
		t.Errorf("after Keep: %v %v", p, err)
	}
	if meta, _ := m.Meta(listURL); meta.UpdatedAt.IsZero() || meta.Mirror != "github" {
		t.Errorf("meta = %+v", meta)
	}

	for _, bad := range []string{"", "not an address", "ftp://x/y", "https://rules.example/a/../b"} {
		if _, err := m.Fetch(context.Background(), bad); !errors.Is(err, ruleurl.ErrAddress) {
			t.Errorf("Fetch(%q) = %v, want ErrAddress", bad, err)
		}
	}
}

func TestReferencesToDatasetsAreFetchedWithTheFile(t *testing.T) {
	n := newNetwork()
	n.put(listURL, []byte("GEOSITE,openai\nGEOIP,telegram\nIP-ASN,13335\nDOMAIN-SUFFIX,example.org\n"))
	n.put(ruleurl.GeoSite("openai"), srsOf(t, "example.net"))
	n.put(ruleurl.GeoIP("telegram"), srsOf(t, "example.com"))
	// the ASN file is missing on purpose
	m := open(t, n, ruledata.GitHub)
	res := m.Update(context.Background(), []string{listURL}, ruledata.UpdateOptions{})
	got := map[string]ruledata.Outcome{}
	for _, r := range res {
		got[r.URL] = r.Outcome
	}
	if len(res) != 4 || got[listURL] != ruledata.Updated || got[ruleurl.GeoSite("openai")] != ruledata.Updated ||
		got[ruleurl.GeoIP("telegram")] != ruledata.Updated || got[ruleurl.ASN("13335")] != ruledata.Failed {
		t.Fatalf("results = %v", got)
	}
	deps := map[string]bool{}
	for _, d := range m.Deps(listURL) {
		deps[d] = true
	}
	if len(deps) != 3 || !deps[ruleurl.GeoSite("openai")] || !deps[ruleurl.GeoIP("telegram")] || !deps[ruleurl.ASN("13335")] {
		t.Errorf("Deps = %v", deps)
	}
	if _, err := m.Parsed(ruleurl.GeoSite("openai")); err != nil {
		t.Errorf("the dataset is stored like any file: %v", err)
	}
}

func TestPrune(t *testing.T) {
	n := newNetwork()
	n.put(jsonURL, jsonRules("example.net"))
	n.put(srsURL, srsOf(t, "example.org"))
	m := open(t, n, ruledata.GitHub)
	m.Update(context.Background(), []string{jsonURL, srsURL}, ruledata.UpdateOptions{})
	if removed := m.Prune(map[string]bool{jsonURL: true}); removed != 1 {
		t.Errorf("removed %d", removed)
	}
	if _, err := m.Data(srsURL); err == nil {
		t.Error("a file nothing refers to is dropped")
	}
	if _, err := m.Parsed(jsonURL); err != nil {
		t.Errorf("a file still referred to stays: %v", err)
	}
}

func TestErrorsNeverRepeatTheAddress(t *testing.T) {
	n := newNetwork()
	n.down["rules.example"] = true
	m := open(t, n, ruledata.GitHub)
	secret := "https://rules.example/x.txt?token=SECRET-TOKEN-VALUE"
	info, err := ruleurl.Canonical(secret)
	if err != nil {
		t.Fatal(err)
	}
	r := m.Update(context.Background(), []string{info.URL}, ruledata.UpdateOptions{})
	if r[0].Outcome != ruledata.Failed || strings.Contains(r[0].Err.Error(), "SECRET-TOKEN-VALUE") || strings.Contains(ruledata.Describe(r[0].Err), "rules.example") {
		t.Errorf("error leaks the address: %v", r[0].Err)
	}
	_, err = m.Fetch(context.Background(), secret)
	if err == nil || strings.Contains(err.Error(), "SECRET-TOKEN-VALUE") {
		t.Errorf("Fetch error leaks the address: %v", err)
	}
}

func TestUpdateIsParallelButOrdered(t *testing.T) {
	n := newNetwork()
	var urls []string
	for i := 0; i < 20; i++ {
		u := fmt.Sprintf("%so/r/main/set%02d.json", raw, i)
		urls = append(urls, u)
		n.put(u, jsonRules(fmt.Sprintf("host%02d.example.net", i)))
	}
	m := open(t, n, ruledata.GitHub)
	res := m.Update(context.Background(), append(urls, urls[0]), ruledata.UpdateOptions{})
	if len(res) != 20 {
		t.Fatalf("a repeated address is fetched once: %d results", len(res))
	}
	for i, r := range res {
		if r.URL != urls[i] || r.Outcome != ruledata.Updated {
			t.Errorf("result %d = %+v", i, r)
		}
	}
	if h := n.hitsOf(urls[0]); h != 1 {
		t.Errorf("hits = %d", h)
	}
}

func TestACancelledUpdateReportsFailures(t *testing.T) {
	n := newNetwork()
	n.put(jsonURL, jsonRules("example.net"))
	m := open(t, n, ruledata.GitHub)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := m.Update(ctx, []string{jsonURL}, ruledata.UpdateOptions{})
	if r[0].Outcome != ruledata.Failed || r[0].Err == nil {
		t.Errorf("%+v", r[0])
	}
}

func TestEarlierVersionsLeaveNothingBehind(t *testing.T) {
	dir := t.TempDir()
	old := []string{"index.json", "geosite-cn.srs", "geoip-cn.srs"}
	for _, f := range old {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(`{"geosite-cn":{"size":1}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(dir, "notes.txt")
	_ = os.WriteFile(keep, []byte("x"), 0o600)
	m, err := ruledata.New(ruledata.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Statuses()) != 0 {
		t.Error("old entries are not carried over")
	}
	for _, f := range old {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("%s from an earlier version should be gone", f)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("files that are not the store's are left alone")
	}
}

func TestMirrorsFor(t *testing.T) {
	names := func(ms []ruledata.Mirror) string {
		var s []string
		for _, m := range ms {
			s = append(s, m.Name)
		}
		return strings.Join(s, ",")
	}
	for _, c := range []struct{ setting, prefix, want string }{
		{"", "", "github,jsdelivr"}, {"auto", "", "github,jsdelivr"}, {"github", "", "github"}, {"jsdelivr", "", "jsdelivr"},
		{"auto", "https://m.example/", "github,jsdelivr,custom"}, {"github", " https://m.example/ ", "github,custom"},
	} {
		if got := names(ruledata.MirrorsFor(c.setting, c.prefix)); got != c.want {
			t.Errorf("MirrorsFor(%q,%q) = %s, want %s", c.setting, c.prefix, got, c.want)
		}
	}
	if got := ruledata.Prefix("https://m.example/").URL(listURL); got != "https://m.example/"+listURL {
		t.Errorf("Prefix = %q", got)
	}
}
