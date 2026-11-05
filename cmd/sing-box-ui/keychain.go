package main

import (
	"errors"

	"github.com/egoist/mygo"

	"github.com/jtsang4/sing-box-ui/internal/library"
)

// keychain keeps the subscription links in the system's credential store:
// the login Keychain on macOS, the Credential Manager on Windows, the Secret
// Service on Linux. A link carries the account's credentials, so it never sits
// in an ordinary file when the system can keep it.
type keychain struct{}

func (keychain) Get(key string) (string, error) {
	v, err := mygo.Secrets.Get(key)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return "", library.ErrNoSecret
	}
	return string(v), err
}

func (keychain) Set(key, value string) error { return mygo.Secrets.Set(key, []byte(value)) }

func (keychain) Delete(key string) error { return mygo.Secrets.Delete(key) }
