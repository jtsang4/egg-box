# The resource ledger

Purpose: after a crash, kill or power loss the next start can remove exactly what the previous run created, and nothing it did not create.

Contract, in one line: **only delete resources we created, and only if they still carry our fingerprint.** Nothing ever resets the route table, flushes DNS wholesale or removes something that was never recorded.

## Entry design

| Field | Meaning |
| --- | --- |
| `id` | unique per entry |
| `kind` | how a driver releases it: tun-interface, dns, route, file (extend as needed) |
| `key` | the resource's stable identity: interface name, dynamic-store key, route destination, file path |
| `token` | the fingerprint the service wrote into the resource, or expects on it (a random value created per session and stored in the resource: a resolver entry's marker, an adapter description, a file's content) |
| `meta` | what the driver needs to release it |
| `created` | timestamp |

A release proceeds only when the live resource still carries the token. If somebody replaced it, it is left alone and forgotten (outcome `NotOurs`).

## Phases and write order

- The ledger has a phase: `clean` (nothing applied) or `active` (resources may exist).
- `BeginSession` writes phase `active` and the session identity **durably before the first resource is created**.
- Each resource is recorded durably (write to a temp file, sync, atomic rename) as soon as it exists and before anything else is done; `Add` returns only after the write is on disk. Drivers record through a `Recorder` handed to `Activate`, so nothing can be created without a record being offered.
- `Cleanup` releases entries newest first. Resolved entries are removed as it goes; failures stay recorded so the next attempt (or a person) can see and retry them. When everything is resolved the ledger is marked clean. `MarkClean` refuses while entries remain.
- A normal disconnect does the same cleanup, so crash recovery and normal shutdown share one code path (and one set of tests).

## Outcomes of releasing one entry

`Released` (existed, ours, removed), `AlreadyGone` (nothing to do), `NotOurs` (fingerprint mismatch: leave it, forget it). Releasing must be idempotent: running cleanup twice, or after a half-finished cleanup, is normal.

## Recovery at start

Open the ledger before anything else. It reports:

- `Needed`: the previous run did not end cleanly (phase `active`) or the file was unreadable;
- `Entries`: what was still recorded;
- `Corrupt`: the file could not be parsed, so ownership is unknown.

If `Needed`, run cleanup on the recorded entries first, then start normally. If `Corrupt`, fall back to a **sweep**: remove leftovers identified only by well-known names, and only when they carry your mark:

- macOS resolver entry: remove it only if it holds the agent's token; an entry under the same key without the mark is somebody else's.
- Windows adapter: remove one of your name only if its description says it is the tunnel driver's adapter; verify that it is gone and, if it is not, tell the person where to remove it by hand.
- A leftover tunnel interface that only its owning process can remove disappears with that process; do not try.

A sweep that fails must be **visible** (status and diagnostics), not silent: a test asserts it.

## The same discipline for the rest of the state

The service's other durable state (configuration revisions, settings, intent) lives in the same protected data directory (created 0700, files 0600) and follows the same rules:

- Write files atomically: temp file in the same directory, write, chmod, fsync, close, rename over the target.
- A configuration revision is staged in `rev-N.pending/` (config plus the resource files it references) and promoted into `rev-N/` by a single rename; a `meta` file holds the `current` and `last good` pointers. Leftover `.pending` directories are swept when the store opens, so a crash mid-save never produces a half-visible revision.
- A corrupt settings or intent file must not stop the service from starting: move it aside (`name.corrupt-<timestamp>`), start from defaults, and report that it happened.
- Size-limit what the GUI may send (a maximum config size) and reject out-of-range values at the boundary.

## Tests worth having

- crash between "resource created" and "recorded" (phase `active`, entry missing): recovery still ends in a clean state;
- corrupt ledger file: falls back to the sweep and removes only marked things;
- release of an entry whose token no longer matches: `NotOurs`, nothing removed;
- cleanup called twice; cleanup after partial failure keeps the failed entries;
- the simulated driver records every call, so tests assert exactly what was created and released.
