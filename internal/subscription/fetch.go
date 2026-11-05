package subscription

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultUserAgent is what the fetcher announces. Providers choose the format
// they return by User-Agent; sing-box's own is the one that yields sing-box JSON.
const DefaultUserAgent = "sing-box/1.14.4 (egg-box)"

// MaxBody caps a subscription download.
const MaxBody = 16 << 20

// FetchOptions tune Fetch.
type FetchOptions struct {
	UserAgent string
	// Timeout covers the whole request; default 30s.
	Timeout time.Duration
	// MaxBytes caps the body; default MaxBody.
	MaxBytes int64
	// ETag and LastModified make the request conditional; a 304 answer comes
	// back as Fetched.NotModified.
	ETag, LastModified string
	// Client replaces the default HTTP client (tests, custom proxies).
	Client *http.Client
}

// Fetched is a downloaded subscription.
type Fetched struct {
	Body         []byte
	NotModified  bool
	ETag         string
	LastModified string
	ContentType  string
	Usage        *Usage
	// Interval is the update interval the provider asks for; 0 when it did not say.
	Interval time.Duration
	Title    string
	HomePage string
}

// FetchErrorKind classifies a failed download.
type FetchErrorKind string

const (
	FetchBadURL   FetchErrorKind = "bad_url"
	FetchNetwork  FetchErrorKind = "network"
	FetchTimeout  FetchErrorKind = "timeout"
	FetchStatus   FetchErrorKind = "http_status"
	FetchTooLarge FetchErrorKind = "too_large"
	FetchTLS      FetchErrorKind = "tls"
	FetchDNS      FetchErrorKind = "dns"
	FetchCanceled FetchErrorKind = "canceled"
)

// FetchError is a download failure. Its message never contains the URL: the
// link is a credential.
type FetchError struct {
	Kind   FetchErrorKind
	Status int
	Cause  string
}

func (e *FetchError) Error() string {
	switch e.Kind {
	case FetchStatus:
		return fmt.Sprintf("the server answered HTTP %d", e.Status)
	case FetchBadURL:
		return "the subscription link is not a valid http(s) URL"
	case FetchTimeout:
		return "the request timed out"
	case FetchTooLarge:
		return "the response is too large"
	case FetchTLS:
		return "the TLS handshake failed: " + e.Cause
	case FetchDNS:
		return "the server name could not be resolved"
	case FetchCanceled:
		return "the request was cancelled"
	}
	if e.Cause != "" {
		return "the request failed: " + e.Cause
	}
	return "the request failed"
}

// ValidateURL checks a subscription link without contacting it.
func ValidateURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return &FetchError{Kind: FetchBadURL}
	}
	return nil
}

// Fetch downloads a subscription.
func Fetch(ctx context.Context, rawURL string, opts FetchOptions) (*Fetched, error) {
	rawURL = strings.TrimSpace(rawURL)
	if err := ValidateURL(rawURL); err != nil {
		return nil, err
	}
	if opts.UserAgent == "" {
		opts.UserAgent = DefaultUserAgent
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = MaxBody
	}
	client := opts.Client
	if client == nil {
		client = defaultClient(opts.Timeout)
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, &FetchError{Kind: FetchBadURL}
	}
	req.Header.Set("User-Agent", opts.UserAgent)
	req.Header.Set("Accept", "*/*")
	if opts.ETag != "" {
		req.Header.Set("If-None-Match", opts.ETag)
	}
	if opts.LastModified != "" {
		req.Header.Set("If-Modified-Since", opts.LastModified)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, classify(ctx, err)
	}
	defer resp.Body.Close()

	out := &Fetched{
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
		ContentType:  resp.Header.Get("Content-Type"),
	}
	hi := readHeaders(resp.Header)
	out.Usage, out.Interval, out.Title, out.HomePage = hi.Usage, hi.Interval, hi.Title, hi.HomePage

	switch {
	case resp.StatusCode == http.StatusNotModified:
		out.NotModified = true
		return out, nil
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, &FetchError{Kind: FetchStatus, Status: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, opts.MaxBytes+1))
	if err != nil {
		return nil, classify(ctx, err)
	}
	if int64(len(body)) > opts.MaxBytes {
		return nil, &FetchError{Kind: FetchTooLarge}
	}
	out.Body = body
	return out, nil
}

func defaultClient(timeout time.Duration) *http.Client {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          2,
		IdleConnTimeout:       30 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("redirect to an unsupported scheme")
			}
			return nil
		},
	}
}

// classify turns a transport error into a FetchError without keeping the URL
// or any address that net/http puts into its messages: only a category of
// cause survives.
func classify(ctx context.Context, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		return &FetchError{Kind: FetchCanceled}
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return &FetchError{Kind: FetchTimeout}
	}
	var to interface{ Timeout() bool }
	if errors.As(err, &to) && to.Timeout() {
		return &FetchError{Kind: FetchTimeout}
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return &FetchError{Kind: FetchDNS}
	}
	var unknown x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var invalid x509.CertificateInvalidError
	switch {
	case errors.As(err, &unknown):
		return &FetchError{Kind: FetchTLS, Cause: "the certificate is not trusted"}
	case errors.As(err, &hostErr):
		return &FetchError{Kind: FetchTLS, Cause: "the certificate does not match the host"}
	case errors.As(err, &invalid):
		return &FetchError{Kind: FetchTLS, Cause: "the certificate is not valid (expired or not yet valid)"}
	}
	msg := strings.ToLower(err.Error())
	cause := ""
	switch {
	case strings.Contains(msg, "tls:") || strings.Contains(msg, "x509:"):
		return &FetchError{Kind: FetchTLS, Cause: "the handshake failed"}
	case strings.Contains(msg, "refused"):
		cause = "the connection was refused"
	case strings.Contains(msg, "unreachable"), strings.Contains(msg, "no route"):
		cause = "the network is unreachable"
	case strings.Contains(msg, "reset"), strings.Contains(msg, "forcibly closed"), strings.Contains(msg, "broken pipe"):
		cause = "the connection was reset"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), strings.Contains(msg, "eof"):
		cause = "the connection was closed"
	case strings.Contains(msg, "redirect"):
		cause = "too many or unsupported redirects"
	}
	return &FetchError{Kind: FetchNetwork, Cause: cause}
}
