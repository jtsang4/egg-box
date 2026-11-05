package rulefmt

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/sagernet/sing-box/common/srs"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

// Split merges any number of rule lists and divides the result into the two
// parts a plan item is compiled to.
//
// The domain part holds every rule that can be decided without knowing the
// destination's address: domains, but also ports, processes and the like.
// The IP part holds the rules that need an address, which the core only has
// after a resolve step. Routing puts all domain parts before all IP parts,
// so traffic that a domain rule catches is never resolved for nothing.
//
// A rule that mixes domain and address conditions is an OR between the two
// in sing-box (both are one "destination address" group), so it splits into
// one rule per part. Inverted and logical rules stay whole and go to the IP
// part when anything inside needs an address.
func Split(sets ...[]option.HeadlessRule) (domainPart, ipPart []option.HeadlessRule) {
	var da, ia addrAcc
	var dom, ip []option.HeadlessRule
	for _, set := range sets {
		for _, r := range set {
			placeRule(r, &da, &ia, &dom, &ip)
		}
	}
	if r, ok := da.rule(); ok {
		dom = append([]option.HeadlessRule{r}, dom...)
	}
	if r, ok := ia.rule(); ok {
		ip = append([]option.HeadlessRule{r}, ip...)
	}
	return dom, ip
}

type addrAcc struct {
	domain, suffix, keyword, regex, adguard, ipcidr []string
}

func (a *addrAcc) rule() (option.HeadlessRule, bool) {
	d := option.DefaultHeadlessRule{
		Domain: uniq(a.domain), DomainSuffix: uniq(a.suffix), DomainKeyword: uniq(a.keyword),
		DomainRegex: uniq(validRegexes(a.regex)), AdGuardDomain: uniq(a.adguard), IPCIDR: uniq(a.ipcidr),
	}
	if !d.IsValid() {
		return option.HeadlessRule{}, false
	}
	return option.HeadlessRule{Type: C.RuleTypeDefault, DefaultOptions: d}, true
}

func placeRule(r option.HeadlessRule, da, ia *addrAcc, dom, ip *[]option.HeadlessRule) {
	switch r.Type {
	case C.RuleTypeLogical:
		if needsAddress(r) {
			*ip = append(*ip, r)
		} else {
			*dom = append(*dom, r)
		}
		return
	case "", C.RuleTypeDefault:
	default:
		return
	}
	d := r.DefaultOptions
	hasDomain := len(d.Domain)+len(d.DomainSuffix)+len(d.DomainKeyword)+len(d.DomainRegex)+len(d.AdGuardDomain) > 0
	hasIP := len(d.IPCIDR) > 0
	if !hasDomain && !hasIP && !d.IsValid() {
		return
	}
	r.Type = C.RuleTypeDefault
	if d.Invert {
		if hasIP {
			*ip = append(*ip, r)
		} else {
			*dom = append(*dom, r)
		}
		return
	}
	if !hasOtherConditions(d) {
		if hasDomain {
			da.domain = append(da.domain, d.Domain...)
			da.suffix = append(da.suffix, d.DomainSuffix...)
			da.keyword = append(da.keyword, d.DomainKeyword...)
			da.regex = append(da.regex, d.DomainRegex...)
			da.adguard = append(da.adguard, d.AdGuardDomain...)
		}
		if hasIP {
			ia.ipcidr = append(ia.ipcidr, d.IPCIDR...)
		}
		return
	}
	switch {
	case hasDomain && hasIP:
		onlyDomain := r
		onlyDomain.DefaultOptions.IPCIDR = nil
		onlyDomain.DefaultOptions.IPSet = nil
		*dom = append(*dom, onlyDomain)
		onlyIP := r
		o := &onlyIP.DefaultOptions
		o.Domain, o.DomainSuffix, o.DomainKeyword, o.DomainRegex, o.AdGuardDomain = nil, nil, nil, nil, nil
		o.DomainMatcher, o.AdGuardDomainMatcher = nil, nil
		*ip = append(*ip, onlyIP)
	case hasIP:
		*ip = append(*ip, r)
	default:
		*dom = append(*dom, r)
	}
}

// hasOtherConditions reports whether a default rule constrains anything
// besides the destination address; such a rule cannot be merged into the
// shared list, because its conditions are ANDed with the address.
func hasOtherConditions(d option.DefaultHeadlessRule) bool {
	return len(d.QueryType) > 0 || len(d.Network) > 0 || len(d.SourceIPCIDR) > 0 || d.SourceIPSet != nil ||
		len(d.SourcePort) > 0 || len(d.SourcePortRange) > 0 || len(d.Port) > 0 || len(d.PortRange) > 0 ||
		len(d.ProcessName) > 0 || len(d.ProcessPath) > 0 || len(d.ProcessPathRegex) > 0 ||
		len(d.PackageName) > 0 || len(d.PackageNameRegex) > 0 || len(d.NetworkType) > 0 ||
		d.NetworkIsExpensive || d.NetworkIsConstrained || len(d.WIFISSID) > 0 || len(d.WIFIBSSID) > 0 ||
		(d.NetworkInterfaceAddress != nil && d.NetworkInterfaceAddress.Size() > 0) || len(d.DefaultInterfaceAddress) > 0
}

// needsAddress reports whether a rule can only be decided once the
// destination's address is known.
func needsAddress(r option.HeadlessRule) bool {
	if r.Type == C.RuleTypeLogical {
		return slices.ContainsFunc(r.LogicalOptions.Rules, needsAddress)
	}
	return len(r.DefaultOptions.IPCIDR) > 0
}

// Compile writes rules as the .srs file the running core reads. It returns
// nil for an empty list. The file is deterministic: equal rules give equal
// bytes, which the resource store relies on for content addressing.
func Compile(rules []option.HeadlessRule) (out []byte, err error) {
	if len(rules) == 0 {
		return nil, nil
	}
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("%w: %v", ErrMalformed, r)
		}
	}()
	var buf bytes.Buffer
	if err := srs.Write(&buf, option.PlainRuleSet{Rules: rules}, C.RuleSetVersionCurrent); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Digest names a compiled file by its content.
func Digest(srsBytes []byte) string {
	sum := sha256.Sum256(srsBytes)
	return hex.EncodeToString(sum[:])
}
