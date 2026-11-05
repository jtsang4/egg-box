package singbox

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/adapter/certificate"
	"github.com/sagernet/sing-box/adapter/service"
	"github.com/sagernet/sing-box/option"

	"eggbox/internal/agent/policy"
)

// These tests are the guard rail for upgrading sing-box: the privileged core
// is only as safe as the agreement between what the policy lets through and
// what the compiled registries can instantiate.

func TestPolicyAllowListsMatchRegistries(t *testing.T) {
	for _, server := range []bool{false, true} {
		reg := RegistryOptions{ServerInbounds: server}
		p := policy.New(PolicyOptions(reg))

		check := func(kind string, policyTypes, registryTypes []string) {
			t.Helper()
			sort.Strings(policyTypes)
			sort.Strings(registryTypes)
			if !reflect.DeepEqual(policyTypes, registryTypes) {
				t.Errorf("server inbounds=%v: %s mismatch\n policy:   %v\n registry: %v", server, kind, policyTypes, registryTypes)
			}
		}
		check("inbound", p.InboundTypes(), InboundRegistry(reg).OptionTypes())
		check("outbound", p.OutboundTypes(), OutboundRegistry().OptionTypes())
		check("endpoint", p.EndpointTypes(), EndpointRegistry().OptionTypes())
		check("dns transport", p.DNSServerTypes(), DNSTransportRegistry().OptionTypes())
	}
}

func TestNoServicesOrCertificateProvidersAreRegistered(t *testing.T) {
	if got := service.NewRegistry().OptionTypes(); len(got) != 0 {
		t.Errorf("service registry must stay empty, has %v", got)
	}
	if got := certificate.NewRegistry().OptionTypes(); len(got) != 0 {
		t.Errorf("certificate provider registry must stay empty, has %v", got)
	}
}

// TestPathLikeFieldsAreClassified walks every option struct the registries can
// create and fails if a string field that looks like a file or program
// reference is not one the policy deliberately classifies. Unknown ones are
// still rejected at runtime (deny by default); this test makes sure a sing-box
// upgrade that adds one is noticed and decided on, not silently broken.
func TestPathLikeFieldsAreClassified(t *testing.T) {
	reg := RegistryOptions{ServerInbounds: true}
	roots := []reflect.Type{reflect.TypeFor[option.Options]()}
	add := func(types []string, create func(string) (any, bool)) {
		for _, name := range types {
			if o, ok := create(name); ok {
				roots = append(roots, reflect.TypeOf(o))
			}
		}
	}
	in := InboundRegistry(reg)
	add(in.OptionTypes(), in.CreateOptions)
	out := OutboundRegistry()
	add(out.OptionTypes(), out.CreateOptions)
	ep := EndpointRegistry()
	add(ep.OptionTypes(), ep.CreateOptions)
	dnsReg := DNSTransportRegistry()
	add(dnsReg.OptionTypes(), dnsReg.CreateOptions)

	unknown := map[string][]string{}
	seen := map[reflect.Type]bool{}
	var walk func(owner string, ty reflect.Type)
	walk = func(owner string, ty reflect.Type) {
		for ty.Kind() == reflect.Ptr || ty.Kind() == reflect.Slice || ty.Kind() == reflect.Array || ty.Kind() == reflect.Map {
			ty = ty.Elem()
		}
		if ty.Kind() != reflect.Struct || seen[ty] {
			return
		}
		seen[ty] = true
		for i := 0; i < ty.NumField(); i++ {
			f := ty.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			switch {
			case name == "-" || (f.Anonymous && name == ""):
				// Payloads hidden behind custom unmarshalers (rules, rule sets, inbounds).
				walk(owner, f.Type)
				continue
			case name == "":
				continue
			}
			if stringLike(f.Type) && policy.PathLikeKey(name) && !policy.KnownPathKey(name) {
				unknown[name] = append(unknown[name], ty.Name())
			}
			walk(owner, f.Type)
		}
	}
	for _, r := range roots {
		walk(r.Name(), r)
	}
	for key, owners := range unknown {
		sort.Strings(owners)
		t.Errorf("path-like field %q (in %v) is not classified by the policy: decide whether it is forbidden, a res: reference or harmless", key, owners)
	}
}

func stringLike(t reflect.Type) bool {
	for t.Kind() == reflect.Slice || t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.Kind() == reflect.String
}
