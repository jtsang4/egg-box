package library_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"eggbox/internal/library"
	"eggbox/internal/routing"
	"eggbox/internal/subscription"
)

// linkList renders share links of invented servers, one per name.
func linkList(names ...string) string {
	var b strings.Builder
	for i, n := range names {
		host := strings.ToLower(strings.ReplaceAll(strings.Fields(n)[0], "_", "")) + string(rune('a'+i)) + ".example.net"
		b.WriteString("trojan://pw" + string(rune('a'+i)) + "@" + host + ":443?sni=" + host + "#" + strings.ReplaceAll(n, " ", "%20") + "\n")
	}
	return b.String()
}

type provider struct {
	srv    *httptest.Server
	body   atomic.Value // string
	status atomic.Int32
	hits   atomic.Int32
	header http.Header
}

func newProvider(t *testing.T, body string) *provider {
	t.Helper()
	p := &provider{header: http.Header{}}
	p.body.Store(body)
	p.status.Store(200)
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.hits.Add(1)
		if code := int(p.status.Load()); code != 200 {
			http.Error(w, "nope", code)
			return
		}
		for k, v := range p.header {
			w.Header()[k] = v
		}
		if tag := w.Header().Get("ETag"); tag != "" && r.Header.Get("If-None-Match") == tag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte(p.body.Load().(string)))
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// link is the subscription URL, with a path that looks like a credential.
func (p *provider) link() string { return p.srv.URL + "/sub/9f3a7c1d5e8b4a60" }

func open(t *testing.T, dir string, now func() time.Time) *library.Library {
	t.Helper()
	l, err := library.Open(dir, library.Options{Secrets: library.NewMemorySecrets(), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func openWith(t *testing.T, dir string, secrets library.SecretStore) *library.Library {
	t.Helper()
	l, err := library.Open(dir, library.Options{Secrets: secrets})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestAddRemoteKeepsTheLinkOutOfFiles(t *testing.T) {
	dir := t.TempDir()
	p := newProvider(t, linkList("HK 01", "JP 01"))
	secrets := library.NewMemorySecrets()
	l := openWith(t, dir, secrets)

	sub, err := l.AddRemote("", p.link(), library.RemoteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sub.Kind != library.KindRemote || !sub.Enabled || sub.IntervalHours != library.DefaultIntervalHours || sub.Name == "" || sub.Host != "127.0.0.1" {
		t.Fatalf("%+v", sub)
	}
	if _, err := l.Refresh(context.Background(), sub.ID, library.RefreshOptions{Client: p.srv.Client()}); err != nil {
		t.Fatal(err)
	}

	got, err := l.Link(sub.ID)
	if err != nil || got != p.link() {
		t.Fatalf("link = %q, %v", got, err)
	}
	// No file under the directory may contain the link or its token.
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), "9f3a7c1d5e8b4a60") {
			t.Errorf("%s contains the subscription link", path)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v, want 0600", path, info.Mode().Perm())
		}
		return nil
	})

	if _, err := l.AddRemote("x", "ftp://example.com/sub", library.RemoteOptions{}); err == nil {
		t.Error("a non-http link was accepted")
	}
	if _, err := l.AddRemote("x", p.link(), library.RemoteOptions{Include: "("}); err == nil {
		t.Error("an invalid filter was accepted")
	}
}

func TestRefreshStoresNodesAndProviderMetadata(t *testing.T) {
	p := newProvider(t, linkList("HK 01", "HK 02", "JP 01"))
	p.header.Set("Subscription-Userinfo", "upload=100; download=200; total=1000; expire=1893456000")
	p.header.Set("Profile-Update-Interval", "12")
	p.header.Set("Profile-Title", "Fast Lane")
	p.header.Set("ETag", `"abc"`)
	l := open(t, t.TempDir(), nil)
	sub, _ := l.AddRemote("", p.link(), library.RemoteOptions{})

	res, err := l.Refresh(context.Background(), sub.ID, library.RefreshOptions{Client: p.srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if res.NodeCount != 3 || !res.Changed || res.Added != 3 || res.Removed != 0 {
		t.Fatalf("%+v", res)
	}
	got, _ := l.Subscription(sub.ID)
	if got.NodeCount != 3 || got.Name != "Fast Lane" || got.IntervalHours != 12 || !got.IntervalAuto || got.ETag != `"abc"` {
		t.Errorf("%+v", got)
	}
	if got.Usage == nil || got.Usage.Total != 1000 || got.Usage.Used() != 300 || got.Usage.Expire.IsZero() {
		t.Errorf("usage %+v", got.Usage)
	}
	if got.UpdatedAt.IsZero() || got.LastError != "" || got.Format == "" {
		t.Errorf("%+v", got)
	}

	// A second refresh with the validator finds nothing new and does not re-download the body.
	res, err = l.Refresh(context.Background(), sub.ID, library.RefreshOptions{Client: p.srv.Client()})
	if err != nil || !res.NotModified || res.Changed {
		t.Fatalf("%+v, %v", res, err)
	}
	// The provider changes its list: one node is replaced.
	p.body.Store(linkList("HK 01", "HK 02", "SG 01"))
	p.header.Del("ETag")
	res, err = l.Refresh(context.Background(), sub.ID, library.RefreshOptions{Client: p.srv.Client(), Force: true})
	if err != nil || !res.Changed || res.Added != 1 || res.Removed != 1 {
		t.Fatalf("%+v, %v", res, err)
	}

	// Editing the interval by hand stops the provider from changing it.
	if err := l.Edit(sub.ID, func(s *library.Subscription) { s.IntervalHours = 6 }); err != nil {
		t.Fatal(err)
	}
	if got, _ := l.Subscription(sub.ID); got.IntervalAuto || got.IntervalHours != 6 {
		t.Errorf("%+v", got)
	}
}

func TestFailedRefreshKeepsNodesAndNeverLeaksTheLink(t *testing.T) {
	p := newProvider(t, linkList("HK 01", "JP 01"))
	l := open(t, t.TempDir(), nil)
	sub, _ := l.AddRemote("A", p.link(), library.RemoteOptions{})
	if _, err := l.Refresh(context.Background(), sub.ID, library.RefreshOptions{Client: p.srv.Client()}); err != nil {
		t.Fatal(err)
	}

	for name, fail := range map[string]func(){
		"server error": func() { p.status.Store(503) },
		"garbage":      func() { p.status.Store(200); p.body.Store("<html>please log in</html>") },
		"empty":        func() { p.status.Store(200); p.body.Store("") },
	} {
		fail()
		_, err := l.Refresh(context.Background(), sub.ID, library.RefreshOptions{Client: p.srv.Client()})
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
		got, _ := l.Subscription(sub.ID)
		if got.LastError == "" || strings.Contains(got.LastError, "9f3a7c1d5e8b4a60") || strings.Contains(err.Error(), "9f3a7c1d5e8b4a60") {
			t.Errorf("%s: last error %q / %v", name, got.LastError, err)
		}
		if nodes, _ := l.Nodes(sub.ID); len(nodes) != 2 || got.NodeCount != 2 {
			t.Errorf("%s: the previous nodes were not kept (%d)", name, len(nodes))
		}
	}

	// Recovery clears the error.
	p.status.Store(200)
	p.body.Store(linkList("HK 01", "JP 01", "US 01"))
	if _, err := l.Refresh(context.Background(), sub.ID, library.RefreshOptions{Client: p.srv.Client()}); err != nil {
		t.Fatal(err)
	}
	if got, _ := l.Subscription(sub.ID); got.LastError != "" || got.NodeCount != 3 {
		t.Errorf("%+v", got)
	}
}

func TestActiveNodesApplyFiltersAndMergeSubscriptions(t *testing.T) {
	pa := newProvider(t, linkList("HK 01", "HK 02 Netflix", "JP 01"))
	// "HK 01" here is another server than A's: same name, different host.
	pb := newProvider(t, strings.ReplaceAll(linkList("HK 01", "SG 01"), "hka.example.net", "hk-b.example.net"))
	l := open(t, t.TempDir(), nil)
	a, _ := l.AddRemote("A", pa.link(), library.RemoteOptions{Exclude: "netflix"})
	b, _ := l.AddRemote("B", pb.link(), library.RemoteOptions{})
	for _, x := range []struct {
		id string
		p  *provider
	}{{a.ID, pa}, {b.ID, pb}} {
		if _, err := l.Refresh(context.Background(), x.id, library.RefreshOptions{Client: x.p.srv.Client()}); err != nil {
			t.Fatal(err)
		}
	}

	nodes, err := l.ActiveNodes()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	identities := map[string]bool{}
	for _, n := range nodes {
		names = append(names, n.SubName+":"+n.Node.Name)
		id := n.ID()
		if identities[id.Key()] {
			t.Errorf("two nodes with the identity %q", id.Key())
		}
		identities[id.Key()] = true
		if id.Sub != n.SubID || id.Name != n.Node.Name || id.Hash != n.Key() {
			t.Errorf("identity %+v of %s", id, n.Node.Name)
		}
		if !n.Auto {
			t.Errorf("%s: a subscription takes part in the automatic choice unless told otherwise", n.Node.Name)
		}
	}
	// A: HK 01, JP 01 (Netflix excluded); B: HK 01, SG 01. The providers' names are kept:
	// the two "HK 01" are told apart by their subscriptions, not by renaming one of them.
	if len(nodes) != 4 {
		t.Fatalf("got %d nodes: %v", len(nodes), names)
	}
	if nodes[0].SubName != "A" || nodes[2].SubName != "B" {
		t.Errorf("order or owners wrong: %v", names)
	}
	if nodes[0].Node.Name != "HK 01" || nodes[2].Node.Name != "HK 01" {
		t.Errorf("names were changed: %v", names)
	}

	// Disabling a subscription removes its nodes; an include filter narrows the other.
	if err := l.Edit(a.ID, func(s *library.Subscription) { s.Enabled = false }); err != nil {
		t.Fatal(err)
	}
	if err := l.Edit(b.ID, func(s *library.Subscription) { s.Include = "^sg" }); err != nil {
		t.Fatal(err)
	}
	nodes, _ = l.ActiveNodes()
	if len(nodes) != 1 || nodes[0].Node.Name != "SG 01" {
		t.Fatalf("%+v", nodes)
	}
	if err := l.Edit(b.ID, func(s *library.Subscription) { s.Include = "(" }); err == nil {
		t.Error("an invalid filter was accepted by Edit")
	}
}

func TestIdenticalServersInTwoSubscriptionsAreOneNode(t *testing.T) {
	body := linkList("HK 01", "JP 01")
	pa, pb := newProvider(t, body), newProvider(t, body)
	l := open(t, t.TempDir(), nil)
	for _, p := range []*provider{pa, pb} {
		s, _ := l.AddRemote("", p.link(), library.RemoteOptions{})
		if _, err := l.Refresh(context.Background(), s.ID, library.RefreshOptions{Client: p.srv.Client()}); err != nil {
			t.Fatal(err)
		}
	}
	if nodes, _ := l.ActiveNodes(); len(nodes) != 2 {
		t.Fatalf("got %d nodes, want the same two servers once", len(nodes))
	}
}

func TestPersistenceAcrossOpen(t *testing.T) {
	dir := t.TempDir()
	secrets := library.NewMemorySecrets()
	p := newProvider(t, linkList("HK 01", "JP 01"))
	l := openWith(t, dir, secrets)
	sub, _ := l.AddRemote("Main", p.link(), library.RemoteOptions{UserAgent: "clash.meta", Exclude: "x"})
	if _, err := l.Refresh(context.Background(), sub.ID, library.RefreshOptions{Client: p.srv.Client()}); err != nil {
		t.Fatal(err)
	}
	prefs := l.Prefs()
	prefs.LANDirect = false
	prefs.MixedPort = 7890
	if _, err := l.SetPrefs(prefs); err != nil {
		t.Fatal(err)
	}
	if err := l.UpdateRouting(func(st *routing.State) error {
		if err := st.SetMain(routing.ToRegion("JP")); err != nil {
			return err
		}
		_, err := st.AddException(routing.Exception{Kind: routing.EntryDomainSuffix, Value: "example.org", Dest: routing.Direct(), Enabled: true})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.Edit(sub.ID, func(s *library.Subscription) { s.NoAuto, s.Alias = true, "  Main\tVPS  " }); err != nil {
		t.Fatal(err)
	}
	if err := l.UpdateSettings(func(s *library.Settings) {
		s.Language, s.Theme, s.RuleMirrorPrefix = "zh", "dark", "https://gh-proxy.example/"
	}); err != nil {
		t.Fatal(err)
	}

	l2 := openWith(t, dir, secrets)
	got, ok := l2.Subscription(sub.ID)
	if !ok || got.Name != "Main" || got.UserAgent != "clash.meta" || got.NodeCount != 2 || !got.NoAuto || got.Alias != "Main VPS" {
		t.Fatalf("%+v", got)
	}
	if nodes, err := l2.Nodes(sub.ID); err != nil || len(nodes) != 2 {
		t.Fatalf("%d nodes, %v", len(nodes), err)
	}
	if pr := l2.Prefs(); pr.LANDirect || pr.MixedPort != 7890 {
		t.Errorf("%+v", pr)
	}
	if st := l2.Routing(); st.Main.Kind != routing.DestRegion || st.Main.Region != "JP" || len(st.Exceptions) != 1 || st.Exceptions[0].Value != "example.org" {
		t.Errorf("%+v", st)
	}
	if s := l2.Settings(); s.Language != "zh" || s.Theme != "dark" || s.RuleMirrorPrefix != "https://gh-proxy.example/" || !s.AutoRefresh || s.RuleUpdateHours != library.DefaultRuleUpdateHours {
		t.Errorf("%+v", s)
	}
	if link, err := l2.Link(sub.ID); err != nil || link != p.link() {
		t.Errorf("link after reopen: %q, %v", link, err)
	}
}

func TestRemoveDeletesEverythingAboutASubscription(t *testing.T) {
	dir := t.TempDir()
	secrets := library.NewMemorySecrets()
	p := newProvider(t, linkList("HK 01"))
	l := openWith(t, dir, secrets)
	sub, _ := l.AddRemote("A", p.link(), library.RemoteOptions{})
	if _, err := l.Refresh(context.Background(), sub.ID, library.RefreshOptions{Client: p.srv.Client()}); err != nil {
		t.Fatal(err)
	}
	if err := l.Remove(sub.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Subscription(sub.ID); ok {
		t.Error("still listed")
	}
	if _, err := secrets.Get("subscription/" + sub.ID); !errors.Is(err, library.ErrNoSecret) {
		t.Errorf("the link survived: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "nodes")); len(entries) != 0 {
		t.Errorf("node files left: %v", entries)
	}
	if err := l.Remove(sub.ID); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("second remove: %v", err)
	}
}

func TestMoveReorders(t *testing.T) {
	l := open(t, t.TempDir(), nil)
	var ids []string
	for _, n := range []string{"A", "B", "C"} {
		s, err := l.AddRemote(n, "https://example.com/"+n, library.RemoteOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.ID)
	}
	if err := l.Move(ids[2], 0); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, s := range l.Subscriptions() {
		order = append(order, s.Name)
	}
	if strings.Join(order, "") != "CAB" {
		t.Errorf("order %v", order)
	}
	_ = l.Move(ids[2], 99)
	order = nil
	for _, s := range l.Subscriptions() {
		order = append(order, s.Name)
	}
	if strings.Join(order, "") != "ABC" {
		t.Errorf("order %v", order)
	}
}

func TestDue(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	l := open(t, t.TempDir(), func() time.Time { return now })
	p := newProvider(t, linkList("HK 01"))
	a, _ := l.AddRemote("A", p.link(), library.RemoteOptions{})
	manual, _ := l.AddRemote("M", p.link(), library.RemoteOptions{})
	if err := l.Edit(manual.ID, func(s *library.Subscription) { s.IntervalHours = 0 }); err != nil {
		t.Fatal(err)
	}
	if due := l.Due(); len(due) != 1 || due[0] != a.ID {
		t.Fatalf("a never-refreshed subscription is due, a manual one is not: %v", due)
	}
	if _, err := l.Refresh(context.Background(), a.ID, library.RefreshOptions{Client: p.srv.Client()}); err != nil {
		t.Fatal(err)
	}
	if due := l.Due(); len(due) != 0 {
		t.Fatalf("just refreshed: %v", due)
	}
	now = now.Add(25 * time.Hour)
	if due := l.Due(); len(due) != 1 {
		t.Fatalf("a day later: %v", due)
	}

	// After a failure the retry comes sooner than the interval.
	p.status.Store(500)
	_, _ = l.Refresh(context.Background(), a.ID, library.RefreshOptions{Client: p.srv.Client()})
	if due := l.Due(); len(due) != 0 {
		t.Fatalf("right after a failure: %v", due)
	}
	now = now.Add(library.RetryAfterFailure + time.Minute)
	if due := l.Due(); len(due) != 1 {
		t.Fatalf("after the retry delay: %v", due)
	}
	// Disabled subscriptions are never due.
	_ = l.Edit(a.ID, func(s *library.Subscription) { s.Enabled = false })
	if due := l.Due(); len(due) != 0 {
		t.Fatalf("disabled: %v", due)
	}
}

func TestAddLocalAndSettingsValidation(t *testing.T) {
	l := open(t, t.TempDir(), nil)
	res, err := subscription.Parse([]byte(linkList("HK 01", "JP 01")))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := l.AddLocal("Pasted", res)
	if err != nil || sub.Kind != library.KindLocal || sub.NodeCount != 2 {
		t.Fatalf("%+v, %v", sub, err)
	}
	if _, err := l.Refresh(context.Background(), sub.ID, library.RefreshOptions{}); err == nil {
		t.Error("a local subscription must not refresh")
	}
	if due := l.Due(); len(due) != 0 {
		t.Errorf("local subscriptions are never due: %v", due)
	}
	if _, err := l.AddLocal("Empty", &subscription.Result{}); err == nil {
		t.Error("an empty import was accepted")
	}

	for name, fn := range map[string]func(*library.Settings){
		"language": func(s *library.Settings) { s.Language = "xx" },
		"theme":    func(s *library.Settings) { s.Theme = "pink" },
		"mirror":   func(s *library.Settings) { s.RuleMirror = "ftp" },
		"sort":     func(s *library.Settings) { s.SortNodes = "random" },
		"hours":    func(s *library.Settings) { s.RuleUpdateHours = -1 },
		"prefix":   func(s *library.Settings) { s.RuleMirrorPrefix = "ftp://mirror.example/" },
	} {
		if err := l.UpdateSettings(fn); err == nil {
			t.Errorf("%s: an invalid value was accepted", name)
		}
	}
	if s := l.Settings(); s.Language != "" || s.Theme != "" {
		t.Errorf("a refused change was applied: %+v", s)
	}
	// A routing change that fails leaves nothing behind.
	err = l.UpdateRouting(func(st *routing.State) error {
		if err := st.SetMain(routing.ToRegion("US")); err != nil {
			return err
		}
		_, err := st.AddException(routing.Exception{Kind: routing.EntryIPCIDR, Value: "not-an-ip", Dest: routing.Direct(), Enabled: true})
		return err
	})
	if err == nil {
		t.Error("an invalid exception was accepted")
	}
	if st := l.Routing(); st.Main.Kind == routing.DestRegion || len(st.Exceptions) != 0 {
		t.Errorf("a refused routing change was kept: %+v", st)
	}
}

func TestChangeListenersRunOutsideTheLock(t *testing.T) {
	l := open(t, t.TempDir(), nil)
	var calls atomic.Int32
	l.OnChange(func() {
		calls.Add(1)
		_ = l.Settings() // would deadlock if called under the write lock
		_ = l.Subscriptions()
	})
	_ = l.UpdateSettings(func(s *library.Settings) { s.Theme = "dark" })
	_, _ = l.AddRemote("A", "https://example.com/a", library.RemoteOptions{})
	if calls.Load() != 2 {
		t.Errorf("listener ran %d times", calls.Load())
	}
}

func TestFutureFormatIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "library.json"), []byte(`{"version": 99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := library.Open(dir, library.Options{Secrets: library.NewMemorySecrets()}); err == nil {
		t.Error("a library from the future was opened")
	}
	if err := os.WriteFile(filepath.Join(dir, "library.json"), []byte(`{broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := library.Open(dir, library.Options{Secrets: library.NewMemorySecrets()}); err == nil {
		t.Error("a damaged library was opened")
	}
}

func TestSecretStores(t *testing.T) {
	dir := t.TempDir()
	file := library.NewFileSecrets(filepath.Join(dir, "sub", "secrets.json"))
	if _, err := file.Get("k"); !errors.Is(err, library.ErrNoSecret) {
		t.Fatalf("%v", err)
	}
	if err := file.Set("k", "v"); err != nil {
		t.Fatal(err)
	}
	if v, err := file.Get("k"); err != nil || v != "v" {
		t.Fatalf("%q, %v", v, err)
	}
	if info, err := os.Stat(filepath.Join(dir, "sub", "secrets.json")); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Errorf("%v, %v", info, err)
	}
	_ = file.Delete("k")
	if _, err := file.Get("k"); !errors.Is(err, library.ErrNoSecret) {
		t.Errorf("%v", err)
	}

	// A chain falls back when the first store refuses, and cleans stale copies.
	mem := library.NewMemorySecrets()
	chain := library.Chain{refusing{}, mem}
	if err := chain.Set("a", "1"); err != nil {
		t.Fatal(err)
	}
	if v, err := chain.Get("a"); err != nil || v != "1" {
		t.Fatalf("%q, %v", v, err)
	}
	_ = chain.Delete("a")
	if _, err := chain.Get("a"); err == nil {
		t.Error("deleted secret still readable")
	}
	if err := (library.Chain{}).Set("a", "1"); err == nil {
		t.Error("an empty chain accepted a secret")
	}
}

type refusing struct{}

func (refusing) Get(string) (string, error) { return "", errors.New("keychain locked") }
func (refusing) Set(string, string) error   { return errors.New("keychain locked") }
func (refusing) Delete(string) error        { return errors.New("keychain locked") }
