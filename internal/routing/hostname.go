package routing

import (
	"strings"

	"golang.org/x/net/idna"
)

// asciiHost writes a domain the way the engine sees it: names that are not
// in ASCII in the form they are looked up in (a person types 例子.com, the
// connection says xn--fsqu00a.com), and without the dot that ends a name.
func asciiHost(v string) string {
	v = strings.TrimSuffix(v, ".")
	for _, r := range v {
		if r >= 0x80 {
			if a, err := idna.ToASCII(v); err == nil {
				return a
			}
			break
		}
	}
	return v
}

// validHostName says whether v (already normalized) can be the name of a
// host: labels of letters, digits, hyphens and underscores, the last of them
// not made of digits alone (which also leaves out the address 10.0.0.1, and
// a port).
func validHostName(v string) bool {
	if v == "" || len(v) > 253 {
		return false
	}
	labels := strings.Split(v, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			if !hostByte(l[i]) {
				return false
			}
		}
	}
	return !allDigits(labels[len(labels)-1])
}

// validKeyword says whether v can be a part of a host name.
func validKeyword(v string) bool {
	if v == "" {
		return false
	}
	for i := 0; i < len(v); i++ {
		if !hostByte(v[i]) && v[i] != '.' {
			return false
		}
	}
	return true
}

func hostByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_'
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}
