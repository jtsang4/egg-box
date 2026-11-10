package subscription

import (
	"errors"
	"testing"
)

func TestEditedLink(t *testing.T) {
	was := Node{Name: "HK 2", Link: "socks5://127.0.0.1:2#HK"}
	for _, tc := range []struct {
		link, name string
		port       int
	}{
		{"socks5://127.0.0.1:3#HK", "HK 2", 3},           // the name the import gave stays
		{" socks5://127.0.0.1:3#Backup ", "Backup", 3},   // a name the person writes is theirs
		{"socks5://127.0.0.1:3", "SOCKS 127.0.0.1:3", 3}, // no name: one is made up
	} {
		n, err := EditedLink(was, tc.link)
		if err != nil || n.Name != tc.name || n.Port != tc.port || n.Link == "" || n.Link[0] == ' ' {
			t.Errorf("%q: %+v, %v", tc.link, n, err)
		}
	}
	// A link that names nothing is named after where it points now.
	unnamed := Node{Name: "SOCKS 127.0.0.1:2", Link: "socks5://127.0.0.1:2"}
	if n, _ := EditedLink(unnamed, "socks5://127.0.0.1:9"); n.Name != "SOCKS 127.0.0.1:9" {
		t.Errorf("name %q", n.Name)
	}
	// Without the old link there is nothing to compare: the link's name counts.
	if n, _ := EditedLink(Node{Name: "HK 2"}, "socks5://127.0.0.1:3#HK"); n.Name != "HK" {
		t.Errorf("name %q", n.Name)
	}
	for _, bad := range []string{"", " \n ", "socks5://127.0.0.1:1#A\nsocks5://127.0.0.1:2#B"} {
		if _, err := EditedLink(was, bad); !errors.Is(err, ErrNotOneLink) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := EditedLink(was, "gopher://x"); err == nil {
		t.Error("an unknown scheme was read")
	}
}

func TestWithoutLinks(t *testing.T) {
	in := []Node{{Name: "A", Link: "socks5://127.0.0.1:1#A"}}
	out := WithoutLinks(in)
	if out[0].Link != "" || in[0].Link == "" {
		t.Fatalf("in %+v out %+v", in, out)
	}
}
