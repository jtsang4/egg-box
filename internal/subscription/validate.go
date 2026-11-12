package subscription

import (
	"encoding/json"
	"errors"
	"math"
	"strings"

	"golang.org/x/crypto/ssh"
)

// The checks below cover what the core would otherwise refuse only when it
// starts, where the refusal cannot be traced to one node: every path into a
// node (share link, Clash, sing-box JSON) meets them in Validate.

// exactInt reads a JSON integer. A string or a fraction is not one: the core
// would refuse it.
func exactInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1<<31 {
			return int(t), true
		}
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return int(i), true
		}
	}
	return 0, false
}

func validateSnell(cfg map[string]any) error {
	psk, _ := cfg["psk"].(string)
	version, _ := exactInt(cfg["version"])
	obfs, hasObfs := cfg["obfs_mode"]
	mode, hasMode := cfg["mode"]
	switch version {
	case 4:
		if hasMode {
			return errors.New("Snell v4 has no mode")
		}
		if s, ok := obfs.(string); hasObfs && (!ok || (s != "" && s != "none" && s != "http" && s != "tls")) {
			return errors.New("the Snell obfs mode is not supported")
		}
		if _, ok := cfg["obfs_host"]; ok {
			if _, isString := cfg["obfs_host"].(string); !isString {
				return errors.New("the Snell obfs host is not text")
			}
		}
	case 6:
		if l := len(psk); l < 12 || l > 255 {
			return errors.New("a Snell v6 PSK must be 12 to 255 bytes")
		}
		if hasObfs || cfg["obfs_host"] != nil {
			return errors.New("Snell v6 has no obfs")
		}
		if s, ok := mode.(string); hasMode && (!ok || (s != "" && s != "default" && s != "unshaped" && s != "unsafe-raw")) {
			return errors.New("the Snell v6 mode is not supported")
		}
	default:
		return errors.New("the Snell version must be 4 or 6")
	}
	return nil
}

// stringList reads a value the core takes as one string or a list of them.
func stringList(v any) ([]string, bool) {
	switch t := v.(type) {
	case nil:
		return nil, true
	case string:
		return []string{t}, true
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	case []string:
		return t, true
	}
	return nil, false
}

func validateSSH(cfg map[string]any) error {
	password, _ := cfg["password"].(string)
	keys, ok := stringList(cfg["private_key"])
	if !ok {
		return errors.New("the SSH private key cannot be read")
	}
	key := strings.Join(keys, "\n")
	if password == "" && strings.TrimSpace(key) == "" {
		return errors.New("a password or private key is missing")
	}
	if _, present := cfg["private_key"]; present {
		passphrase, _ := cfg["private_key_passphrase"].(string)
		_, err := ssh.ParsePrivateKey([]byte(key))
		var missing *ssh.PassphraseMissingError
		// An encrypted key is only checked for its shape here: decrypting it
		// costs a key derivation on every generation. The import decrypted it.
		if err != nil && !(passphrase != "" && errors.As(err, &missing)) {
			return errors.New("the SSH private key cannot be read")
		}
	}
	hostKeys, ok := stringList(cfg["host_key"])
	if !ok {
		return errors.New("an SSH host key cannot be read")
	}
	for _, k := range hostKeys {
		if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k)); err != nil {
			return errors.New("an SSH host key cannot be read")
		}
	}
	return nil
}
