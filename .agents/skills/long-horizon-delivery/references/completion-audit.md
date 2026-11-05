# Completion audit

Before reporting a big task as complete, build this table from the pinned requirement list (not from memory) and fill every row with evidence you can point to.

## Template

| # | Requirement or constraint | Evidence (command or artifact, result, commit) | Status |
| --- | --- | --- | --- |
| 1 | Feature X works end to end | `go test -tags ... ./e2e` passed at `abc1234`; screenshot matrix reviewed | done |
| 2 | UI is native, no JavaScript | CI step "no JavaScript" passes; `git ls-files` has no `.js/.ts/package.json` | done |
| 3 | Never affect the host network | host snapshot identical before and after at `abc1234`; sandbox driver only | done |
| 4 | Secrets never committed | `secret-guard.sh audit` clean at `abc1234` (history, messages, working tree) | done |
| 5 | Works on Windows service / UAC | cross-compiled and vetted, pure rules unit-tested; no real Windows run | **documented, not verified** |
| 6 | License terms of the embedded core | README section; owner must choose a license and settle the product name | **open decision** |

Status vocabulary: `done` (evidence at the reported commit), `documented, not verified` (cannot be verified here; says how), `open decision` (belongs to the owner), `not done` (say why). Nothing else.

## Rules

- Evidence is at the **commit being reported**. Anything verified earlier and touched since is re-verified or marked stale.
- A requirement with no evidence is `not done`, however sure you feel.
- Quality bars need a measurement or a review record, not an adjective: "reviewed 94 screenshots over three rounds against the rubric; the last round found only items listed in follow-ups".
- Constraints are audited too, not only features: network untouched, no push, owner files not committed, technology excluded, secrets absent.
- Look for requirements that exist only implicitly: a README that tells the truth, installers that clean up after themselves, an uninstall path, a way to see what the service did.

## Report shape

Short, in the user's language:

1. Outcome in one or two sentences.
2. What was verified, by tier, with the commit hash.
3. Findings the user should know (defects found late, dependencies upgraded, behaviour decided where the design was silent).
4. Open decisions (license, name, signing, anything that was not mine to decide).
5. What needs a real machine, pointing to the README table.
6. Where things are (commits, branches, artifacts), and that nothing was pushed if that was the instruction.

Do not list every file or every test. Do not repeat the requirement list; give the table's exceptions.

## Close-out checks

- No background processes left (by pid) and no temp files with real data.
- Host snapshot identical to the baseline.
- `git status` clean except files the owner asked to keep out; no commits pushed unless asked.
- Goal / todo tracking updated to the final state.
