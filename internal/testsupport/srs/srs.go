// Package srs builds small binary sing-box rule sets for tests, so the real
// core can load the same kind of file the application ships as a resource.
package srs

import (
	"bytes"
	"testing"

	"github.com/sagernet/sing-box/common/srs"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

func build(t testing.TB, rule option.DefaultHeadlessRule) []byte {
	t.Helper()
	set := option.PlainRuleSet{Rules: []option.HeadlessRule{{Type: C.RuleTypeDefault, DefaultOptions: rule}}}
	var buf bytes.Buffer
	if err := srs.Write(&buf, set, C.RuleSetVersionCurrent); err != nil {
		t.Fatalf("build rule set: %v", err)
	}
	return buf.Bytes()
}

// DomainSuffixes is a rule set matching the given domain suffixes.
func DomainSuffixes(t testing.TB, suffixes ...string) []byte {
	t.Helper()
	return build(t, option.DefaultHeadlessRule{DomainSuffix: suffixes})
}

// CIDRs is a rule set matching the given address ranges.
func CIDRs(t testing.TB, cidrs ...string) []byte {
	t.Helper()
	return build(t, option.DefaultHeadlessRule{IPCIDR: cidrs})
}
