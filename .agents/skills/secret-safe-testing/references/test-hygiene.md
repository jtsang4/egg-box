# Test hygiene with real credentials

Patterns in Go and shell. The shared rule: the only things a test may print about live data are **counts, categories from a fixed vocabulary, and durations**.

## An opt-in live test

Two switches: a build tag, so `go test ./...` never compiles it, and an env var naming a **file**, so the value is never on a command line.

```go
//go:build live

package e2e

func TestLiveSubscription(t *testing.T) {
	path := os.Getenv("APP_TEST_SUBSCRIPTION_FILE")
	if path == "" {
		t.Skip("APP_TEST_SUBSCRIPTION_FILE is not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read the subscription file") // not err: its text holds the path
	}
	nodes, skipped := parse(raw)
	t.Logf("%d nodes, %d skipped; protocols %v", len(nodes), len(skipped), countBy(nodes, protocolOf))

	ok := 0
	for i, n := range nodes {
		t.Run(fmt.Sprintf("node-%d", i), func(t *testing.T) { // index, never the node's name
			if class := probe(n); class != "ok" {
				t.Errorf("failure class %s", class)
			} else {
				ok++
			}
		})
	}
	t.Logf("%d of %d nodes passed", ok, len(nodes))
}
```

Document the command in the README with placeholders only: `APP_TEST_SUBSCRIPTION_FILE=~/.myapp-dev/subscription.url go test -tags live -run LiveSubscription -v ./...`.

## Say a class, never the error text

Error strings carry what you are hiding: `*url.Error` prints the full URL, `*net.OpError` prints `dial tcp <ip>:<port>`, TLS errors print the server name. Map to a fixed vocabulary and print only that:

```go
func classify(err error) string {
	var ne net.Error
	var dns *net.DNSError
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	case errors.As(err, &dns):
		return "dns"
	case strings.Contains(err.Error(), "tls:"), strings.Contains(err.Error(), "x509:"):
		return "tls"
	default:
		return "other"
	}
}
```

When a failure is unexplained, add more **counts** (how many per class, how long they took) before you weaken an assertion, and never "just print the error once".

## Counting instead of showing

- Count by protocol, by region class, by failure class; print durations rounded.
- Assertions on content use derived booleans: "every node has a non-empty server" is `if n.Server == ""`, not a log of servers.
- A test that must show *which* item failed uses the item's index in the input.

## Invented fixtures

| Kind | Use |
| --- | --- |
| Domains | `example.net`, `example.org`, anything under `.invalid` or `.test` |
| IPv4 | `192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24` (RFC 5737) |
| IPv6 | `2001:db8::/32` (RFC 3849) |
| UUIDs | `00000000-0000-0000-0000-000000000000` or an obviously patterned one |
| Keys, passwords | patterned and obviously fake (`AAAA...`); never a real key "shortened" |
| Names | invented, with different scripts and emoji to exercise the UI |

Do not "anonymize" real data by editing a few characters: it stays re-identifiable. Fixtures are written from scratch.

## Redaction that you can trust

Diagnostics, exports and bug reports leave the machine, so they redact by default:

```go
type Redactor struct {
	seen map[string]string
	next map[string]int
}

func (r *Redactor) placeholder(kind, value string) string {
	key := kind + "\x00" + value
	if p, ok := r.seen[key]; ok {
		return p // the same value always gets the same placeholder: reports stay readable
	}
	r.next[kind]++
	p := fmt.Sprintf("%s-%d", kind, r.next[kind])
	r.seen[key] = p
	return p
}

func keepAddress(a netip.Addr) bool { // needed to diagnose, and not secret
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsUnspecified()
}
```

- Recognize addresses with `netip.ParseAddr` on tokens, not with a loose regex: dotted version strings (`1.14.4`, `0.14.1`) and file names look like IPv4 prefixes to a regex and get mangled. If a regex is unavoidable, allow-list version-shaped strings.
- Redact hosts and URLs by structure (scheme, host, userinfo, query values), then free text.
- Test it with known secrets:

```go
func TestRedactionLeavesNoKnownSecret(t *testing.T) {
	secrets := []string{"203.0.113.77", "node.fake.invalid", "11111111-2222-3333-4444-555555555555"}
	out := NewRedactor().Redact("connect " + strings.Join(secrets, " via ") + " and 192.168.1.5")
	for i, s := range secrets {
		if strings.Contains(out, s) {
			t.Errorf("secret %d survived redaction", i) // the index, not the value
		}
	}
	if !strings.Contains(out, "192.168.1.5") {
		t.Error("private addresses are needed to diagnose and must stay")
	}
}
```

## Shell habits

- No `set -x` in scripts that touch secrets; no secret on a command line (`ps` shows it): read from a file or a pipe.
- Capture the output of live runs into a file under a private temp directory, **scan it**, and read it only if clean:

```bash
out="$(mktemp -d)/live.txt"
go test -tags live -run LiveSubscription -v ./... > "$out" 2>&1
.agents/skills/secret-safe-testing/scripts/secret-guard.sh scan --blocklist "$BLOCKLIST" "$out" && sed -n 1,80p "$out"
```

- Delete such files when done. Do not leave live-run output in `/tmp` for the next session to find.
- Do not open screenshots of live data; do not save them in tracked or synced folders.
