package transport

import (
	"errors"
	"strings"
	"testing"
)

const (
	owner = "S-1-5-21-1004336348-1177238915-682003330-1001"
	other = "S-1-5-21-1004336348-1177238915-682003330-1002"
	gui   = `C:\Program Files\Sing-Box UI\Sing-Box UI.exe`
)

func TestPipeSDDL(t *testing.T) {
	got, err := PipeSDDL(owner)
	if err != nil {
		t.Fatal(err)
	}
	want := "D:P(D;;GA;;;NU)(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;" + owner + ")"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	// The protected flag stops inheritance, and the deny entry comes first.
	if !strings.HasPrefix(got, "D:P(D;;") {
		t.Errorf("the descriptor must be protected and deny network logons first: %s", got)
	}
	for _, bad := range []string{"", "S-1-5", "S-1-5-21-1)(A;;GA;;;WD", "everyone", "S-1-5-21-1-2-3 ", "S-1-5-21-x"} {
		if _, err := PipeSDDL(bad); err == nil {
			t.Errorf("PipeSDDL(%q) accepted a bad identifier", bad)
		}
	}
}

func TestPeerPolicy(t *testing.T) {
	signed := func(path string) error {
		if strings.Contains(path, "Unsigned") {
			return errors.New("no signature")
		}
		return nil
	}
	p := PeerPolicy{OwnerSID: owner, Images: []string{gui}}
	ps := PeerPolicy{OwnerSID: owner, Images: []string{gui, `C:\Program Files\Sing-Box UI\Unsigned.exe`}, RequireSigned: true, Signed: signed}

	cases := []struct {
		name   string
		policy PeerPolicy
		facts  PeerFacts
		ok     bool
	}{
		{"the application of the owner", p, PeerFacts{PID: 4242, SID: owner, Image: gui}, true},
		{"spelled differently", p, PeerFacts{SID: strings.ToLower(owner), Image: `c:/program files/sing-box ui/SING-BOX UI.EXE`}, true},
		{"long path prefix", p, PeerFacts{SID: owner, Image: `\\?\C:\Program Files\Sing-Box UI\Sing-Box UI.exe`}, true},
		{"another user", p, PeerFacts{SID: other, Image: gui}, false},
		{"the owner's other program", p, PeerFacts{SID: owner, Image: `C:\Users\me\Downloads\evil.exe`}, false},
		{"a look-alike directory", p, PeerFacts{SID: owner, Image: `C:\Program Files\Sing-Box UI\..\Sing-Box UI Evil\Sing-Box UI.exe`}, false},
		{"no image known", p, PeerFacts{SID: owner}, false},
		{"signed requirement met", ps, PeerFacts{SID: owner, Image: gui}, true},
		{"signed requirement not met", ps, PeerFacts{SID: owner, Image: `C:\Program Files\Sing-Box UI\Unsigned.exe`}, false},
		{"no owner configured", PeerPolicy{Images: []string{gui}}, PeerFacts{SID: owner, Image: gui}, false},
		{"signature required but cannot be checked", PeerPolicy{OwnerSID: owner, Images: []string{gui}, RequireSigned: true}, PeerFacts{SID: owner, Image: gui}, false},
	}
	for _, c := range cases {
		peer, err := c.policy.Check(c.facts)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok = %v", c.name, err, c.ok)
			continue
		}
		if c.ok && !strings.Contains(peer.Description, "sid=") {
			t.Errorf("%s: the log line says %q", c.name, peer.Description)
		}
	}
}
