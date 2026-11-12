package subscription

import (
	"strings"

	"golang.org/x/crypto/ssh"
)

// sshSpec is an SSH server used as a proxy, as a Clash entry or a share link
// describes it.
type sshSpec struct {
	Server     string
	Port       int
	User       string
	Password   string
	PrivateKey string // the key itself; a file path cannot be followed
	Passphrase string
	HostKeys   []string
	HostKeyAlg []string
}

func (s sshSpec) node(name string) (Node, error) {
	if s.Port == 0 {
		s.Port = 22
	}
	cfg := map[string]any{"type": "ssh", "server": s.Server, "server_port": s.Port}
	setIf(cfg, "user", s.User)
	setIf(cfg, "password", s.Password)
	if key := strings.TrimSpace(s.PrivateKey); key != "" {
		if !strings.Contains(key, "PRIVATE KEY") {
			return Node{}, skip(SkipUnsupportedFeature, "SSH keys read from a file are not supported; paste the key itself")
		}
		var err error
		if s.Passphrase == "" {
			_, err = ssh.ParsePrivateKey([]byte(key))
		} else {
			_, err = ssh.ParsePrivateKeyWithPassphrase([]byte(key), []byte(s.Passphrase))
		}
		if err != nil {
			return Node{}, skip(SkipInvalid, "the SSH private key cannot be read")
		}
		cfg["private_key"] = key
		setIf(cfg, "private_key_passphrase", s.Passphrase)
	}
	if len(s.HostKeys) > 0 {
		var keys []string
		for _, k := range s.HostKeys {
			if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k)); err != nil {
				return Node{}, skip(SkipInvalid, "an SSH host key cannot be read")
			}
			keys = append(keys, k)
		}
		cfg["host_key"] = anySlice(keys)
	}
	if len(s.HostKeyAlg) > 0 {
		cfg["host_key_algorithms"] = anySlice(s.HostKeyAlg)
	}
	return Node{Name: name, Type: "ssh", Server: s.Server, Port: s.Port, Config: cfg}, nil
}

// parseSSH reads ssh://user:password@host:port#name.
func parseSSH(raw string) (Node, error) {
	l, ok := splitShare(raw)
	if !ok {
		return Node{}, skip(SkipInvalid, "not a valid link")
	}
	if l.Host == "" {
		return Node{}, skip(SkipInvalid, "the server address is empty")
	}
	if l.PortSpec != "" && (l.Port < 1 || l.Port > 65535) {
		return Node{}, skip(SkipInvalid, "the port is missing or out of range")
	}
	user, password, _ := l.user()
	return sshSpec{
		Server:     l.Host,
		Port:       l.Port,
		User:       user,
		Password:   password,
		PrivateKey: l.param("private-key", "private_key"),
		Passphrase: l.param("private-key-passphrase", "private_key_passphrase"),
		HostKeys:   splitList(l.param("host-key", "host_key")),
		HostKeyAlg: splitList(l.param("host-key-algorithms", "host_key_algorithms")),
	}.node(nodeName(l))
}
