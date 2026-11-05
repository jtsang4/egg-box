# Drift tests

The gate is only as safe as the agreement between what it lets through and what the engine can instantiate. Both change when a dependency is upgraded, so tests must notice the day they disagree. These tests turn "upgrade the engine" from a risky leap into a checklist.

## 1. The allow-list equals the registries

```go
func TestPolicyAllowListsMatchRegistries(t *testing.T) {
	p := policy.New(policyOptions())
	check := func(kind string, policyTypes, registryTypes []string) {
		t.Helper()
		sort.Strings(policyTypes)
		sort.Strings(registryTypes)
		if !reflect.DeepEqual(policyTypes, registryTypes) {
			t.Errorf("%s mismatch\n policy:   %v\n registry: %v", kind, policyTypes, registryTypes)
		}
	}
	check("inbound", p.InboundTypes(), inboundRegistry().OptionTypes())
	check("outbound", p.OutboundTypes(), outboundRegistry().OptionTypes())
	// ... endpoints, DNS transports
}
```

Build the engine's registries yourself, with only what you mean to ship (an explicit list of registered types, not "everything the library has"), and let the test fail when either side moves. A new protocol in the library does not become reachable until you add it to both on purpose.

Registries you intend to keep empty get an explicit test (`len(serviceRegistry.OptionTypes()) == 0`).

## 2. Every path-like field is classified

Walk every option struct reachable from the engine's registries by reflection, collect string-like fields whose JSON name looks like a file or program reference, and fail if the gate does not classify them:

```go
func TestPathLikeFieldsAreClassified(t *testing.T) {
	// option.Options is the engine's root configuration type; the registries
	// create the per-protocol option structs it cannot name statically.
	roots := []reflect.Type{reflect.TypeFor[option.Options]()}
	for _, name := range outboundRegistry().OptionTypes() {
		if o, ok := outboundRegistry().CreateOptions(name); ok {
			roots = append(roots, reflect.TypeOf(o))
		}
	}
	// ... the same for inbounds, endpoints, DNS transports

	unknown := map[string][]string{}
	seen := map[reflect.Type]bool{}
	var walk func(ty reflect.Type)
	walk = func(ty reflect.Type) {
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
				walk(f.Type) // payloads behind custom unmarshalers
				continue
			case name == "":
				continue
			}
			if stringLike(f.Type) && policy.PathLikeKey(name) && !policy.KnownPathKey(name) {
				unknown[name] = append(unknown[name], ty.Name())
			}
			walk(f.Type)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	for key, owners := range unknown {
		t.Errorf("path-like field %q (in %v) is not classified: decide whether it is forbidden, a res: reference or harmless", key, owners)
	}
}
```

Semantics worth copying:

- At runtime an **unclassified path-like key is already refused** (deny by default). The test is there so an upgrade that adds one is noticed and decided on, not discovered as a user's broken profile.
- Classification categories: *forbidden* (never allowed), *resource reference* (`res:<name>`, rewritten into the protected directory), *matcher* (a rule condition that only compares strings, such as a process path to match), *contextual* (the same key is a file in one place and a URL path in another).
- The key-name heuristic is a word match on `_`-delimited names: `path`, `file`, `dir`/`directory`, `socket`, `output`, `executable`, `command`, `program`, `script`, `binary` (case-insensitive, singular or plural). Err on the side of matching too much, and let the walker look only at string-like fields (a type check), so numbers and booleans never need classifying.

## 3. Capability tests

Gate options mirror the build tags compiled into the engine (QUIC, WireGuard, uTLS/REALITY, ...). Test both directions: with the capability, a node of that type is accepted; without it, the node is refused with `feature_not_available` at its JSON path. Run the real-engine tests with the release tags in CI so the tag combination you ship is the one checked.

## 4. Upgrade checklist (what these tests give you)

1. Bump the engine.
2. `go test` the drift tests: fix every mismatch deliberately (add to both lists or to neither).
3. Run the fuzz seeds, then a short fuzz session.
4. Run the real-engine integration tests with the release tags.
5. Run the vulnerability scan (the upgrade often fixes some, and may add others).
