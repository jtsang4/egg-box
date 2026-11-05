# Generator, gate, heal

When your own code generates what the gate checks (a configuration built from imported items), make the generator speak exactly the gate's dialect, and prove it with a loop test.

## The generator speaks the dialect

- It emits only constructs the gate accepts: one tunnel inbound whose addressing is left to the privileged side, loopback listeners, rule sets only as submitted resources (`res:<name>`), no management APIs.
- It never copies the provider's inbounds, DNS, rules or listeners: provider content contributes **items** (nodes), not structure.
- It reports what it set aside as warnings with a stable code and a detail (for example a rule set that could not be provided), rather than failing silently.
- Because the gate and the generator share a dialect, "generated configuration is accepted" is a testable property, not hope.

## Healing: refusals are explained by items

Provider content is the one thing you do not control. A single odd node (a field the gate refuses, a protocol this build lacks) must not make the whole subscription unusable. So:

1. The gate reports each refusal with a JSON path and a stable code.
2. `Heal(input, output, issues)` maps the paths back to the **items** that produced them (the generator knows which output element came from which node) and returns the input without those items, a list of what was dropped (name, key, reason), and whether anything changed.
3. The caller regenerates and re-checks. The UI then shows "N nodes were left out" with reasons.
4. A refusal that **no item explains** is a bug in the generator or the gate and must surface as one.

## The loop test (a fuzz test over arbitrary provider content)

```go
func FuzzProviderContent(f *testing.F) {
	// seeds: share links, base64 of them, YAML proxies, JSON outbounds,
	// one with a path escape in a certificate field, duplicates, junk
	pol := newPolicyWithReleaseCapabilities()
	f.Fuzz(func(t *testing.T, data []byte) {
		res, err := subscription.Parse(data)
		if err != nil {
			return
		}
		in := configgen.Input{Nodes: res.Nodes, Prefs: configgen.DefaultPrefs()}
		for round := 0; round < 12; round++ {
			out, err := configgen.Generate(in)
			if err != nil {
				return // nothing usable is an answer, not a failure
			}
			result := pol.Normalize(out.JSON, envFor(t, out))
			if !result.HasErrors() {
				return
			}
			healed, _, changed := configgen.Heal(in, out, pathIssues(result))
			if !changed {
				// Say where and why; what the provider sent stays out of the message.
				t.Fatalf("the gate refused a generated configuration and no item explains it: %v", pathIssues(result))
			}
			in = healed
		}
		t.Fatal("leaving out the items the gate refuses did not terminate")
	})
}
```

Properties: (1) every refusal is explained by some item that can be left out; (2) healing terminates (each round removes at least one item, so the bound of rounds is generous); (3) arbitrary bytes never crash the parser. Because the gate is the real one with the real capabilities, this also finds generator mistakes (an invalid combination of options) that unit tests miss.

## Parsing third-party formats

- Support the formats providers really send: share links (one per line), base64 of the whole list, Clash-style YAML, native JSON outbounds. Detect, do not ask.
- Be lenient in reading, strict in the gate. Hand-roll the pieces where the standard library is stricter than the wild: query strings with `;`, unpadded or URL-safe base64, names with unusual unicode, ports given as strings.
- Return a result with the accepted items **and** what was set aside: `Skipped` entries carry a stable code from a small vocabulary (`unsupported_protocol`, `unsupported_transport`, `unsupported_feature`, `invalid`, `info_entry`, `duplicate`) so the UI can explain each in the user's language, and `Notices` carries the provider's announcements (remaining traffic, expiry) to show as text rather than as selectable items.
- Recognize pseudo-entries conservatively. A strict label pattern at the start of the name ("remaining:", "expires:", their translations) is enough on its own; a loose keyword match ("traffic", "reset") only counts together with a placeholder server, so a real node whose name merely contains such a word is not thrown away.
- De-duplicate by identity (same server, same settings), keep the original order, and give the survivors unique, non-empty names.
- Validate each item again where it enters the generator (required fields per protocol, port range): it is the last line of defence before the gate, and its failures are `invalid` skips rather than a refused configuration.
- Never log an item: log counts and codes (`secret-safe-testing`).
