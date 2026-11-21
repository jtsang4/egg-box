package subscription

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Format names the shape a provider's file had.
type Format string

const (
	FormatSingBox     Format = "sing-box"
	FormatClash       Format = "clash"
	FormatLinks       Format = "links"
	FormatBase64Links Format = "base64-links"
)

// Errors returned by Parse. They carry no content of the input.
var (
	ErrEmpty         = errors.New("the subscription is empty")
	ErrUnknownFormat = errors.New("the subscription format is not recognised")
	ErrNoNodes       = errors.New("the subscription contains no usable nodes")
)

// Result is what Parse found.
type Result struct {
	Format Format
	// Nodes are unique and uniquely named.
	Nodes []Node
	// Skipped lists servers that could not become nodes, with the reason.
	Skipped []Skipped
	// Notices are the texts of fake "nodes" providers use to display remaining
	// traffic, expiry and support links.
	Notices []string
}

// Parse recognises a subscription's format and extracts its nodes. When the
// file is understood but holds no usable node it returns the Result together
// with ErrNoNodes, so the reasons in Skipped can be shown.
func Parse(data []byte) (*Result, error) {
	return parse(data, 0)
}

var (
	reClashProxies = regexp.MustCompile(`(?m)^(proxies|proxy-providers)\s*:`)
)

func parse(data []byte, depth int) (*Result, error) {
	data = trimBOM(data)
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, ErrEmpty
	}
	var (
		nodes   []Node
		skipped []Skipped
		format  Format
		err     error
	)
	switch {
	case trimmed[0] == '{' || trimmed[0] == '[':
		format = FormatSingBox
		nodes, skipped, err = parseSingBox(trimmed)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnknownFormat, err)
		}
	case reClashProxies.Match(trimmed):
		format = FormatClash
		nodes, skipped, err = parseClash(trimmed)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnknownFormat, err)
		}
	case bytes.Contains(trimmed, []byte("://")):
		format = FormatLinks
		nodes, skipped = parseLinks(string(trimmed))
	default:
		if depth > 0 {
			return nil, ErrUnknownFormat
		}
		dec, ok := decodeBase64(string(trimmed))
		if !ok {
			return nil, ErrUnknownFormat
		}
		inner, err := parse(dec, depth+1)
		if inner != nil {
			if inner.Format == FormatLinks {
				inner.Format = FormatBase64Links
			}
			return inner, err
		}
		return nil, err
	}

	res := &Result{Format: format, Skipped: skipped}
	var real []Node
	for _, n := range nodes {
		if isInfoEntry(n) {
			res.Notices = append(res.Notices, n.Name)
			res.Skipped = append(res.Skipped, Skipped{Name: n.Name, Type: n.Type, Code: SkipInfoEntry})
			continue
		}
		real = append(real, n)
	}
	kept, dropped := Dedupe(real)
	for i := 0; i < dropped; i++ {
		res.Skipped = append(res.Skipped, Skipped{Code: SkipDuplicate})
	}
	kept, relayDropped := reconcileRelays(kept)
	res.Skipped = append(res.Skipped, relayDropped...)
	res.Nodes = kept
	if len(kept) == 0 {
		return res, ErrNoNodes
	}
	return res, nil
}

var (
	// reInfoStrict matches the labels providers put on their fake nodes.
	reInfoStrict = regexp.MustCompile(`(?i)^\s*(剩余流量|流量剩余|剩余|已用流量|套餐到期|到期时间|过期时间|有效期|距离下次重置|下次重置|流量重置|重置剩余|官网|网址|客服|订阅地址|公告|traffic|data\s+left|remaining|expire[sd]?|expiry|reset|website|official\s+site|telegram|support)\s*[:：|\-]`)
	// reInfoLoose is only trusted together with a placeholder server.
	reInfoLoose = regexp.MustCompile(`(?i)(剩余|流量|到期|过期|重置|套餐|官网|网址|客服|订阅|公告|续费|频道|traffic|expire|remaining|reset|website|telegram|t\.me)`)
)

func isInfoEntry(n Node) bool {
	if reInfoStrict.MatchString(n.Name) {
		return true
	}
	return reInfoLoose.MatchString(n.Name) && placeholderServer(n.Server)
}

func placeholderServer(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", "localhost", "0.0.0.0", "::", "::1", "example.com", "www.example.com":
		return true
	}
	return strings.HasPrefix(s, "127.")
}
