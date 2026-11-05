package subscription

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchHeadersAndBody(t *testing.T) {
	var gotUA, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotAccept = r.UserAgent(), r.Header.Get("Accept")
		w.Header().Set("Subscription-Userinfo", "upload=1; download=2; total=100; expire=2218532293")
		w.Header().Set("Profile-Update-Interval", "6")
		w.Header().Set("Profile-Web-Page-Url", "https://portal.example.com")
		w.Header().Set("Content-Disposition", `attachment; filename="My Provider.yaml"`)
		w.Header().Set("ETag", `"abc"`)
		_, _ = w.Write([]byte("trojan://pw@a.example.com:443#A"))
	}))
	defer srv.Close()

	f, err := Fetch(context.Background(), srv.URL+"/sub?token=SECRET", FetchOptions{UserAgent: "clash.meta"})
	if err != nil {
		t.Fatal(err)
	}
	if gotUA != "clash.meta" || gotAccept == "" {
		t.Fatalf("ua=%q accept=%q", gotUA, gotAccept)
	}
	if string(f.Body) != "trojan://pw@a.example.com:443#A" || f.ETag != `"abc"` {
		t.Fatalf("fetched = %+v", f)
	}
	if f.Usage == nil || f.Usage.Total != 100 || f.Usage.Used() != 3 {
		t.Fatalf("usage = %+v", f.Usage)
	}
	if f.Interval != 6*time.Hour || f.Title != "My Provider" || f.HomePage != "https://portal.example.com" {
		t.Fatalf("meta = %+v", f)
	}
}

func TestFetchDefaultUserAgentAndProfileTitle(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.UserAgent()
		w.Header().Set("Profile-Title", "base64:5oiR55qE5py65Zy6") // 我的机场
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()
	f, err := Fetch(context.Background(), srv.URL, FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ua != DefaultUserAgent || f.Title != "我的机场" {
		t.Fatalf("ua=%q title=%q", ua, f.Title)
	}
}

func TestFetchConditionalRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte("body"))
	}))
	defer srv.Close()
	first, err := Fetch(context.Background(), srv.URL, FetchOptions{})
	if err != nil || first.NotModified || first.ETag != `"v1"` {
		t.Fatalf("first: %+v %v", first, err)
	}
	second, err := Fetch(context.Background(), srv.URL, FetchOptions{ETag: first.ETag})
	if err != nil || !second.NotModified || len(second.Body) != 0 {
		t.Fatalf("second: %+v %v", second, err)
	}
}

func TestFetchRedirectsStatusAndSize(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/short", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/real", http.StatusFound) })
	mux.HandleFunc("/real", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", http.StatusFound) })
	mux.HandleFunc("/ftp", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "ftp://example.com/x", http.StatusFound)
	})
	mux.HandleFunc("/forbidden", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusForbidden) })
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", 5000))) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f, err := Fetch(context.Background(), srv.URL+"/short", FetchOptions{})
	if err != nil || string(f.Body) != "ok" {
		t.Fatalf("redirect: %+v %v", f, err)
	}
	for path, kind := range map[string]FetchErrorKind{"/loop": FetchNetwork, "/ftp": FetchNetwork} {
		if _, err := Fetch(context.Background(), srv.URL+path, FetchOptions{}); !isKind(err, kind) {
			t.Fatalf("%s: %v", path, err)
		}
	}
	_, err = Fetch(context.Background(), srv.URL+"/forbidden", FetchOptions{})
	var fe *FetchError
	if !errors.As(err, &fe) || fe.Kind != FetchStatus || fe.Status != 403 {
		t.Fatalf("status: %v", err)
	}
	if _, err := Fetch(context.Background(), srv.URL+"/big", FetchOptions{MaxBytes: 1000}); !isKind(err, FetchTooLarge) {
		t.Fatalf("size: %v", err)
	}
}

func isKind(err error, kind FetchErrorKind) bool {
	var fe *FetchError
	return errors.As(err, &fe) && fe.Kind == kind
}

func TestFetchTimeoutAndCancel(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	if _, err := Fetch(context.Background(), srv.URL, FetchOptions{Timeout: 150 * time.Millisecond}); !isKind(err, FetchTimeout) {
		t.Fatalf("timeout: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	if _, err := Fetch(ctx, srv.URL, FetchOptions{Timeout: 5 * time.Second}); !isKind(err, FetchCanceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestFetchErrorsNeverContainTheLink(t *testing.T) {
	// A closed port: the connection is refused. net/http would normally print
	// the whole URL (with its token) in the error.
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + "/private/path?token=SUPER-SECRET-TOKEN"
	srv.Close()

	_, err := Fetch(context.Background(), url, FetchOptions{Timeout: 2 * time.Second})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, leak := range []string{"SUPER-SECRET-TOKEN", "/private/path", "127.0.0.1", "localhost"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error leaks %q: %v", leak, err)
		}
	}
}

func TestValidateURL(t *testing.T) {
	for _, ok := range []string{"https://example.com/sub", "http://example.com:8080/x?y=1", "  https://example.com  "} {
		if err := ValidateURL(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "example.com/sub", "file:///etc/passwd", "ftp://example.com", "https://", "javascript:alert(1)", "://x"} {
		if err := ValidateURL(bad); !isKind(err, FetchBadURL) {
			t.Errorf("%q should be rejected: %v", bad, err)
		}
	}
	if _, err := Fetch(context.Background(), "file:///etc/passwd", FetchOptions{}); !isKind(err, FetchBadURL) {
		t.Fatalf("Fetch must refuse non-http schemes: %v", err)
	}
}
