package subscription

import "testing"

func TestSnellLinks(t *testing.T) {
	n := mustLink(t, "snell://test-psk-0001@203.0.113.7:1366?version=6&mode=default&reuse=true#snell-node")
	if n.Type != "snell" || n.Name != "snell-node" || n.Server != "203.0.113.7" || n.Port != 1366 || n.Protocol() != "Snell" {
		t.Fatalf("node = %+v", n)
	}
	eq(t, n, "version", 6)
	eq(t, n, "psk", "test-psk-0001")
	eq(t, n, "reuse", true)
	eq(t, n, "mode", nil) // default is sing-box's own default
	eq(t, n, "obfs_mode", nil)

	n = mustLink(t, "snell://test-psk-0001@203.0.113.7:1366?version=6&mode=unshaped")
	eq(t, n, "mode", "unshaped")

	n = mustLink(t, "snell://pw@v4.example.com:443?version=4&obfs=http&obfs-host=www.example.org#v4")
	eq(t, n, "version", 4)
	eq(t, n, "obfs_mode", "http")
	eq(t, n, "obfs_host", "www.example.org")
	eq(t, n, "reuse", nil)

	// Version 5 is the version 4 wire protocol to sing-box.
	n = mustLink(t, "snell://pw@v5.example.com:443?version=5&obfs=tls")
	eq(t, n, "version", 4)
	eq(t, n, "obfs_mode", "tls")

	// The PSK may also come as a parameter, percent-encoded like any other.
	n = mustLink(t, "snell://v4.example.com:443?version=4&psk=a%2Bb")
	eq(t, n, "psk", "a+b")

	n = mustLink(t, "snell://p%40ss@v4.example.com:443?version=4")
	eq(t, n, "psk", "p@ss")

	wantCode(t, "snell://pw@h.example.com:443", SkipInvalid)                      // no version
	wantCode(t, "snell://pw@h.example.com:443?version=x", SkipInvalid)            // not a number
	wantCode(t, "snell://pw@h.example.com:443?version=3", SkipUnsupportedFeature) // too old
	wantCode(t, "snell://pw@h.example.com:443?version=6", SkipInvalid)            // v6 PSK too short
	wantCode(t, "snell://test-psk-0001@h.example.com:443?version=6&mode=x", SkipUnsupportedFeature)
	wantCode(t, "snell://test-psk-0001@h.example.com:443?version=6&obfs=http", SkipUnsupportedFeature)
	wantCode(t, "snell://pw@h.example.com:443?version=4&obfs=ws", SkipUnsupportedFeature)
	wantCode(t, "snell://@h.example.com:443?version=4", SkipInvalid) // no PSK
	wantCode(t, "snell://pw@h.example.com?version=4", SkipInvalid)   // no port
}

func TestSnellEditedLinkKeepsTheLink(t *testing.T) {
	link := "snell://test-psk-0001@203.0.113.7:1366?version=6&reuse=true#snell-node"
	n, err := EditedLink(Node{}, link)
	if err != nil {
		t.Fatal(err)
	}
	if n.Link != link || n.Type != "snell" || n.Name != "snell-node" {
		t.Fatalf("node = %+v", n)
	}
}

func TestSnellInClashAndSingBoxProfiles(t *testing.T) {
	res, err := Parse([]byte(`proxies:
  - {name: "v4 obfs", type: snell, server: a.example.com, port: "443", psk: pw, version: 4, obfs-opts: {mode: http, host: www.example.org}}
  - {name: "v6", type: snell, server: b.example.com, port: 1366, psk: test-psk-0001, version: 6, reuse: true}
  - {name: "v2", type: snell, server: c.example.com, port: 443, psk: pw, version: 2}
`))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Node{}
	for _, n := range res.Nodes {
		byName[n.Name] = n
	}
	if len(res.Nodes) != 2 || len(res.Skipped) != 1 || res.Skipped[0].Code != SkipUnsupportedFeature {
		t.Fatalf("nodes %v, skipped %+v", names(res.Nodes), res.Skipped)
	}
	eq(t, byName["v4 obfs"], "version", 4)
	eq(t, byName["v4 obfs"], "obfs_mode", "http")
	eq(t, byName["v4 obfs"], "obfs_host", "www.example.org")
	eq(t, byName["v6"], "version", 6)
	eq(t, byName["v6"], "reuse", true)

	res, err = Parse([]byte(`{"outbounds":[{"type":"snell","tag":"sb","server":"d.example.com","server_port":1366,"version":6,"psk":"test-psk-0001"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 1 || res.Nodes[0].Type != "snell" || res.Nodes[0].Name != "sb" {
		t.Fatalf("nodes %+v, skipped %+v", res.Nodes, res.Skipped)
	}
}

func TestSnellFromSingBoxIsValidated(t *testing.T) {
	res, err := Parse([]byte(`{"outbounds":[
	  {"type":"snell","tag":"ok","server":"a.example.com","server_port":1,"version":6,"psk":"test-psk-0001"},
	  {"type":"snell","tag":"short","server":"a.example.com","server_port":2,"version":6,"psk":"short"},
	  {"type":"snell","tag":"v5","server":"a.example.com","server_port":3,"version":5,"psk":"pw"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 1 || res.Nodes[0].Name != "ok" || len(res.Skipped) != 2 {
		t.Fatalf("nodes %v, skipped %+v", names(res.Nodes), res.Skipped)
	}
}
