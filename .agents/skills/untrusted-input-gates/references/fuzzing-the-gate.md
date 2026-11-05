# Fuzzing the gate

The interesting question about a gate is never "does it reject this?". It is "**what does it let through?**" So the fuzz test audits accepted output with an oracle that shares no code with the gate.

## Properties to assert

For every input `raw` and every environment variant (production, sandbox):

1. If the gate refuses (`HasErrors`), it returns **nothing runnable** (empty output).
2. If it accepts, the output satisfies each invariant, checked by walking the whole output tree:
   - the output is a strict document (no duplicate keys), top-level sections are a subset of the allowed ones;
   - fields the privileged side owns hold the privileged side's values (cache path, tunnel addressing, no log output path);
   - every listener is loopback, or the tunnel the privileged side configured; a sandbox result has no tunnel;
   - no forbidden key is set to a non-empty value anywhere, whatever the nesting;
   - every path-like key holds only an allowed form (a path under the resource directory, or a URL-style path), judged by **the key's position and the value**, not by the gate's per-section logic.

The oracle is allowed to be cruder and more paranoid than the gate. It must not call the gate's helpers.

## Shape

```go
func FuzzNormalize(f *testing.F) {
	for _, seed := range seeds { // valid, then attack-shaped
		f.Add([]byte(seed))
	}
	p := newPolicy()
	f.Fuzz(func(t *testing.T, raw []byte) {
		for _, env := range []Env{prodEnv, sandboxEnv} {
			res := p.Normalize(raw, env)
			if res.HasErrors() {
				if len(res.JSON) != 0 {
					t.Fatalf("a refused input must not come with something to run")
				}
				continue
			}
			auditAccepted(t, env, res.JSON)
		}
	})
}

func auditAccepted(t *testing.T, env Env, out []byte) {
	root := mustDecodeStrict(t, out)
	for k := range root {
		if !allowedTop[k] {
			t.Fatalf("top-level section %q must not survive", k)
		}
	}
	walk(t, root, "$", env) // generic walk: types, keys, values, positions
}
```

Report failures with the JSON path and the rule broken; the accepted document goes in the failure message because the test data is invented (never fuzz with real credentials or provider data).

## Seeds

Seeds are valid documents plus attack-shaped ones; they double as regression tests because `go test` runs them without `-fuzz`:

- minimal valid; the same with each privileged field present;
- path escapes (`/etc/passwd`, `../..`, absolute paths in every path-like field, in nested objects and arrays);
- `0.0.0.0` and `::` listeners; management APIs; services; external-program fields; a log output path; a cache path of the attacker's choosing;
- a resource reference that is not in the bundle; a reference with traversal (`res:../x`);
- duplicate keys; wrong shapes (`[]`, `null`, `{}`, a string root, an empty document, binary junk);
- a deeply nested document, a huge number, odd unicode in keys.

## Running

```bash
go test -run 'FuzzNormalize' ./pkg/policy                          # seeds only; what CI runs
go test -run '^$' -fuzz 'FuzzNormalize$' -fuzztime 90s ./pkg/policy  # a local session
```

Only one fuzz target may match per `-fuzz` invocation. Keep the fuzz function fast (milliseconds) so it explores more. A failing input is written under `testdata/fuzz/FuzzNormalize/`.

## Reading a failure

Decide first: **gate bug or oracle bug?**

- Gate bug: fix the gate, keep the input as a seed or as a table test, and add a line to the invariants if it exposes a promise you had not written down.
- Oracle bug (it demanded something the gate never promised, or misjudged a harmless value): fix the oracle, and delete the stale corpus entry that only demonstrated the oracle's mistake. Example from practice: unknown keys inside an allowed outbound object pass through by design (the gate allow-lists types and path-like fields, not every key), so the oracle was refined to check what is actually promised: plain strings in path-like keys.

Never silence the fuzzer by loosening both sides. Record the decision in the commit message.

## Same technique for parsers

For importers of third-party formats, fuzz `parse -> generate -> gate` as a loop (`generate-gate-heal.md`): the assertion is that every refusal is explained by an item that can be left out, and that leaving items out terminates.
