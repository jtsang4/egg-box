---
name: long-horizon-delivery
description: Drives a large, long-running autonomous build (a /goal-style request that turns a design document into a complete, verified product) to an honest finish - pin every requirement and standing constraint, de-risk the scariest assumption first, build in a verification-friendly order, commit by stage, run a tiered verification matrix in a fix loop, re-run it at the final commit, keep auditing after everything is green, and report requirement by requirement including what could not be verified. Use for multi-hour or multi-phase development, when asked to loop verification and fixes until nothing is left, when constraints arrive mid-task, or before declaring a big task complete. 长周期自主开发、验收修复循环、完成审计。
---

# Long-horizon delivery

Big builds fail in predictable ways: a constraint given early is forgotten hours later, green tests hide a tier that never ran, "done" rests on evidence from an earlier commit, and the unverifiable parts are quietly described as working. This is the discipline that prevents those.

## 1. Pin the contract, and keep it alive

- Write every requirement and every constraint as a numbered list, **including follow-ups that arrive mid-task**. Keep it where it survives context loss (the todo list, the goal text, a notes file outside the repo). Re-read it at the start of each phase and before the final report.
- Sort by kind: *must do*; *must not* (hard: touching the user's network, publishing secrets, committing owner-supplied files); *quality bar* (say how it will be measured); *process* (commit per stage, do not push, which files are never committed, which technology is excluded).
- Turn each hard "must not" into a mechanism, not a promise: hooks, sandbox seams, guard tests. See `host-safe-verification` and `secret-safe-testing`.
- Read supplied design material completely before designing. Where it disagrees with reality (versions, APIs), decide, record the decision and its reason, and report it at the end.
- Decide at the start what cannot be verified in this environment, and confine that code: thin, cross-compiled, vetted, with its logic pure and tested elsewhere.

## 2. Remove the scariest unknown first

- Find the assumption that would invalidate the architecture if wrong (calling a native framework from a runtime without a C toolchain, embedding a third-party core in-process, an OS approval flow). Prove it with a proof of concept of tens of lines before designing around it.
- The pinned source of a dependency is ground truth. Locate it (`go list -m -f '{{.Dir}}' <module>`) and grep before relying on a feature that docs or memory claim exists. Check every framework feature you plan to use against the pinned version once, up front.
- Read the licenses of the heavy dependencies at the start (copyleft terms, naming clauses). A late discovery changes the product (`go-release-quality-gates`).

## 3. Build order that stays verifiable

1. Contracts and seams: interfaces, a wire API with a version guard, simulated implementations, fakes.
2. Pure logic with tests: state machines, policy, parsing. Time-driven logic is a pure machine plus a thin timer wrapper.
3. Adapters to real components behind build tags, with integration tests.
4. Product surfaces against fixtures or a demo backend.
5. Platform layers: thin OS code, cross-compiled and vetted.
6. Packaging, CI, documentation.
7. Audit sweeps (section 5).

Each stage ends with: format, vet, tests, the secret and forbidden-content checks, then a commit. Stage files explicitly, commit locally unless told to push, and write messages that say why. Small coherent commits keep history readable and rollback cheap.

## 4. Verify in tiers and loop until clean

Tiers and what each cannot see: `references/verification-matrix.md`. The loop:

1. Run the cheapest tier first.
2. Read the failure; classify it: product bug, test bug, flake, environment.
3. Fix the root cause.
4. Re-run the failing tier and every earlier tier the change could affect.
5. Re-run the **whole matrix at the final commit**. A dependency upgrade, a refactor or a "tiny" fix invalidates earlier green runs.

Rules:

- Do not weaken an assertion before the failure is understood; add count-only diagnostics first.
- A suspiciously fast pass may be a skip. Run with `-v` and count RUN and SKIP lines.
- Every tier must also run somewhere automatic. Compare CI with your local commands, build tags included. (In the project behind this skill CI never ran the tagged tests, and the README's test command did not run the real-core tests.)
- Run timing-sensitive suites alone; concurrent heavy builds cause flakes. Triage procedure: `references/test-triage.md`.
- Never rerun until green.

## 5. Keep looking after green

Green means the existing checks pass, not that the product is right. For each check ask what class of defect it cannot see, then add a sweep for it. Each of these found a real defect in the project behind this skill:

- dependency vulnerabilities with reachability (9 reachable, including a gRPC authorization bypass, fixed by upgrading);
- static analysis for dead code (26 unused declarations, with cascading unused imports and tests);
- licenses and naming terms of embedded components (a copyleft core with a "no implied association" clause);
- the CI configuration itself (actionlint, shellcheck) and the documentation (run the commands it tells people to run);
- a vet across every GOOS/GOARCH and tag set (a dev tag that broke only the Windows build);
- packaging inspected without installing; idle CPU and memory measured;
- host snapshot before and after; secret audit; forbidden-technology audit (for example "no JavaScript files");
- the CLI's help text and error messages, read as a user would.

## 6. Finish honestly

- Completion audit: each requirement against its evidence (command, result, commit) and status. Template: `references/completion-audit.md`.
- Unverifiable is not done. List it with the reason and how to verify it, in the README as well as the report.
- Decisions that belong to the owner (license, product name, signing identity) are reported as open decisions, not made silently.
- The final message is short: outcome, evidence summary, open decisions, what needs a real machine.
- Clean up: scratch files, background processes, temp directories. Leave the host as found (snapshot to prove it).

## Anti-patterns

- Declaring victory from a green run at an earlier commit.
- "Fixing" a flaky test by rerunning, lengthening a sleep, or deleting the assertion.
- Describing code that only compiled for a platform as working there.
- Letting the mid-task constraint live only in the chat.
- Discovering a license or naming problem after the product is named and packaged.
- Parallel tool calls that share processes, ports or directories.

## References

- `references/verification-matrix.md` - tiers, commands, what each cannot see, order.
- `references/test-triage.md` - flaky test procedure with the causes met in practice.
- `references/completion-audit.md` - requirement-to-evidence template and report shape.
- `references/environment-gotchas.md` - shell and tool pitfalls of long unattended sessions.
