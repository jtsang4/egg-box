# Triage of failing and flaky tests

A flaky test is a bug report with missing details. Do not rerun until green, lengthen a sleep, or delete the assertion.

## Procedure

1. **Reproduce with numbers.** `go test -run '^TestX$' -count=30 ./pkg` (add `-race`, and run once while the machine is busy). Note the failure rate; "sometimes" is not a diagnosis.
2. **Classify the failure**: timing (a sleep or deadline), shared resource (port, file, environment variable, global), ordering (maps, goroutines, `t.Parallel`), clock granularity, CPU contention, or a real product race.
3. **Add diagnostics before changing assertions**: counts, timestamps, per-item outcomes (never secrets: see `secret-safe-testing`). The next failure must explain itself.
4. **Fix the cause**: wait on the condition instead of time; make the resource private (port `:0`, `t.TempDir()`, `t.Setenv`); remove the shared global; fix the product race. If the cause is the environment, serialize the competing work and say so in the commit message.
5. **Prove by repetition**: the same `-count=30` run must pass, including under load.
6. **Record the reason** in the commit message and, if it can recur elsewhere, in a code comment at the wait.

## Causes met in practice

- **Mixed clock granularity.** A component reported "last tested at" in whole seconds; the test compared it with a timestamp taken at sub-second precision when the test started and demanded "newer". About half the runs failed. Compare at the coarser granularity, or wait until the value *changes from the previous one*.
- **A reserved port that is not free for the other transport.** A helper reserved a TCP port; the component under test also bound the same number over UDP and sometimes lost it. Reserve both, or let the component bind `:0` and read the port back.
- **CPU contention.** A tight window (two seconds to connect and answer) failed only while another heavy build ran in parallel. Do not run heavy builds beside timing-sensitive suites; widen the *deadline*, never the *sleep*.
- **Fixed sleeps.** Replace with polling of the real condition, 20 ms steps, until a deadline.
- **A multi-item live check.** Many items finish at different times. Wait until all have a result or the deadline passes, then fail only on zero successes or on a class of failure that matters, and report counts per class.
- **Test order and globals.** `t.Parallel` plus a package-level variable, or a test that leaves an environment variable or working directory changed. Use `t.Setenv`, `t.Chdir` (or restore in cleanup), and no mutable globals.

## Passing too quickly

A suite that takes 1.9 seconds when it should drive a real engine for 20 may be skipping. Run `go test -v` and count `--- PASS` against `--- SKIP`; check the build tag and the env var that gate it. Make a gate that skips loudly (`t.Skip("…is not set")`), and make CI fail if a required tier skipped.

## When a failure appears only at the end

If the final matrix fails on something that passed before, do not assume the last change did it: check `git diff` since the last full green run, then bisect by tier. A dependency upgrade, a toolchain change, and a regenerated file are the usual suspects.
