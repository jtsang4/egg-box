---
name: untrusted-input-gates
description: Guides building and testing the gate that decides what untrusted or semi-trusted input (provider-supplied configs, GUI-supplied settings, plugins, templates, imported subscriptions) may make a privileged process do - invariants first, allow-lists, parse once and serialize the same tree, overwrite what the privileged side must own, files as handles not paths, reference checks with located errors, drift tests against the engine's compiled capabilities, fuzzing with an independent oracle, and generator-gate-heal loops. Use when a root/SYSTEM or otherwise trusted component consumes configuration or content produced elsewhere, when importing third-party formats, or when writing fuzz tests for validators. 不可信输入闸门、模糊测试。
---

# Untrusted input gates

When a privileged component consumes configuration or content produced somewhere else (a GUI, a provider, a plugin, a template), put **one gate** in front of it. The gate answers a single question: *what may this input make us run?* Everything below serves that question.

## Design checklist (most important first)

1. **Write the promises as invariants before writing rules.** Examples: no file outside the resource directory is read; listeners are loopback only; no program is executed; no management surface exists; the privileged side owns addressing, routes, DNS, cache and log paths. Tests and the fuzz oracle check exactly these.
2. **Allow-list, never deny-list**: sections, component types, and fields where a type has dangerous ones. Unknown means refuse (or drop with a warning only when harmless by construction).
3. **Parse once; check, rewrite and serialize that same tree.** Never validate one parse and hand the original bytes to the executor: two parsers can disagree (duplicate keys, number forms, encodings). Use a strict decoder: size limit before parsing, object root, duplicate keys rejected, trailing data rejected.
4. **Own the dangerous parts: overwrite, do not validate.** Tunnel addressing, routes, DNS hijack, cache and log paths, listen addresses of anything the privileged side starts: the gate replaces them from the platform plan. A validated value can still be wrong; an overwritten one cannot.
5. **Files are handles, not paths.** A path-like field is refused unless it is a reference (`res:<name>`) to a file the unprivileged side handed over; the gate rewrites it to the privileged side's protected directory and checks that the file is in the bundle. Classify every path-like key on purpose (forbidden, resource reference, matcher that only compares strings, contextual), and **reject path-like keys nobody classified**, so that a dependency upgrade that adds one fails a drift test and forces a decision. Check the field's type: a boolean or number named `*_path` is not a path.
6. **Network surface**: loopback listeners only; no management APIs, services, system-proxy toggling or external-program fields.
7. **Reference integrity at save time**: every tag a rule, group or detour mentions must exist; report it with the JSON path (the engine would only notice when traffic arrives). Account for implicit defaults and built-in tags, or the check produces false errors.
8. **Capabilities follow the build.** Give the gate the protocols the binary was compiled with (build tags). A node needing one that is absent yields `feature_not_available` with its path, not an opaque start failure.
9. **Structured issues**: severity, JSON path, a stable machine code, a message; the UI localizes by code. A refused input yields **no** runnable output. Issue text carries where and why, never the input's content (it may be a secret).
10. **Sandbox as a policy variant** so development uses the same gate: for example it turns "create a TUN" into "listen on loopback", and the oracle asserts that a sandbox result never contains a TUN.
11. **Lenient in, strict at the gate** for third-party formats (next section).

## Importing third-party formats

- Be lenient when reading what providers send, strict at the gate. Share links in the wild have quirks: Go's `url.ParseQuery` rejects a query containing `;` and drops those pairs, so split on `&` yourself and unescape each part. Accept several encodings (plain, base64, YAML, JSON).
- **Report what you skipped, with the reason**; never drop silently. Providers include pseudo-entries ("traffic left", "expires on") that must be recognized and set aside with a reason.
- Keep the provider's inbounds, DNS and rules out of what you generate; produce exactly the dialect the gate accepts (`references/generate-gate-heal.md`).

## Tests

- **Table tests per rule**: both the accepted and the refused form.
- **Drift tests**: the allow-list equals what the engine can instantiate; every path-like field reachable in the engine's option types is classified; registries you meant to be empty are empty. These make dependency upgrades safe (`references/drift-tests.md`).
- **Fuzzing with an independent oracle** that audits what was accepted (`references/fuzzing-the-gate.md`).
- **A generator-gate-heal loop** over arbitrary provider content (`references/generate-gate-heal.md`).
- Seeds run as ordinary tests (no `-fuzz` flag), so CI exercises them on every run.

## Anti-patterns

- Validating a copy and executing the original.
- Scrubbing a config string with regular expressions.
- A deny-list of "dangerous keys".
- Letting the unprivileged side choose a path, a port or an interface name.
- Trusting content because "the user chose that provider".
- An oracle that reuses the gate's logic, so both are wrong together.
- Asserting that bad input is rejected instead of asserting what accepted input can do.
- Hard-coding capabilities instead of deriving them from the build.
- Error messages that quote the input.

## References

- `references/fuzzing-the-gate.md` - the oracle, seeds, what to assert, running it, reading a failure.
- `references/drift-tests.md` - registry equality and a reflection walk that forces classification of new fields.
- `references/generate-gate-heal.md` - generator that speaks the gate's dialect, healing by leaving out offending items, and the loop test.
