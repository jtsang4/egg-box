package rulefmt

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"

	"github.com/sagernet/sing-box/common/convertor/adguard"
	"github.com/sagernet/sing/common/logger"
)

// addDomainLine adds one line of a domain-behavior list (Clash domain
// provider, Surge DOMAIN-SET, a plain domain file).
//
//	+.a.com   a.com and every sub-domain
//	.a.com    Clash: the sub-domains only. Surge: a.com and every sub-domain
//	*.a.com   one label in front of a.com
//	a.com     exactly a.com
func (b *builder) addDomainLine(line string, style Style) {
	s := cleanLine(line)
	if s == "" {
		return
	}
	s = strings.ToLower(strings.TrimSuffix(s, "."))
	switch {
	case strings.HasPrefix(s, "+."):
		rest := s[2:]
		if !isDomainLike(rest) {
			b.ignored++
			return
		}
		b.suffix = append(b.suffix, rest)
	case strings.HasPrefix(s, "*.") && !strings.Contains(s[2:], "*"):
		rest := s[2:]
		if !isDomainLike(rest) {
			b.ignored++
			return
		}
		b.regex = append(b.regex, wildcardRegex(s, `[^.]+`, `[^.]`))
	case strings.HasPrefix(s, "."):
		rest := s[1:]
		if !isDomainLike(rest) {
			b.ignored++
			return
		}
		if style == StyleSurge {
			b.suffix = append(b.suffix, rest)
		} else {
			b.suffix = append(b.suffix, s)
		}
	case strings.Contains(s, "*"):
		if !isDomainLike(strings.ReplaceAll(s, "*", "x")) {
			b.ignored++
			return
		}
		b.regex = append(b.regex, wildcardRegex(s, `[^.]+`, `[^.]`))
	default:
		if !isDomainLike(s) {
			b.ignored++
			return
		}
		b.domain = append(b.domain, s)
	}
}

func parseDomainList(data []byte, style Style) (*Parsed, error) {
	b := &builder{}
	sc := lineScanner(data)
	for sc.Scan() {
		b.addDomainLine(sc.Text(), style)
	}
	return b.finish(FormatDomainList)
}

func parseCIDRList(data []byte) (*Parsed, error) {
	b := &builder{}
	sc := lineScanner(data)
	for sc.Scan() {
		s := cleanLine(sc.Text())
		if s == "" {
			continue
		}
		if !b.addCIDR(&b.ipcidr, s) {
			b.ignored++
		}
	}
	return b.finish(FormatCIDRList)
}

// hostsSkip are the names every hosts file defines for the machine itself.
var hostsSkip = map[string]bool{
	"localhost": true, "localhost.localdomain": true, "local": true, "broadcasthost": true,
	"ip6-localhost": true, "ip6-loopback": true, "ip6-localnet": true, "ip6-mcastprefix": true,
	"ip6-allnodes": true, "ip6-allrouters": true, "ip6-allhosts": true, "0.0.0.0": true,
}

func parseHosts(data []byte) (*Parsed, error) {
	b := &builder{}
	sc := lineScanner(data)
	for sc.Scan() {
		s := cleanLine(sc.Text())
		if s == "" {
			continue
		}
		f := strings.Fields(s)
		if len(f) < 2 {
			b.ignored++
			continue
		}
		for _, h := range f[1:] {
			h = strings.ToLower(h)
			if hostsSkip[h] {
				continue
			}
			if !isDomainLike(h) {
				b.ignored++
				continue
			}
			b.domain = append(b.domain, h)
		}
	}
	return b.finish(FormatHosts)
}

// parseAdGuard reads an Adblock or AdGuard filter with sing-box's own
// converter, so a list means the same here as it does to `sing-box rule-set
// convert --type adguard`: only the domain entries survive.
func parseAdGuard(data []byte) (*Parsed, error) {
	rules, err := adguard.ToOptions(bytes.NewReader(data), logger.NOP())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEmpty, err)
	}
	rules, dropped := normalize(rules)
	if len(rules) == 0 {
		return nil, ErrEmpty
	}
	st := Count(rules)
	st.Ignored = dropped
	return &Parsed{Format: FormatAdGuard, Rules: rules, Stats: st}, nil
}

func lineScanner(data []byte) *bufio.Scanner {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	return sc
}
