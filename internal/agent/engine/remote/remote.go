// Package remote carries an engine's calls between two processes of one
// application: on iOS the core runs in the packet tunnel extension, a process
// of its own that the system starts and stops with the VPN, while the agent
// that decides what it runs stays in the application. Serve answers in the
// extension, Client asks from the application, over a Unix socket in the
// container the two share.
//
// Every call is a connection of its own: a request, an answer, and for the
// streams the messages until either side closes. Frames are a four-byte
// big-endian length and the payload: JSON for requests and answers, the
// agent's protocol buffers for what streams.
package remote

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"eggbox/internal/agent/engine"
)

// maxFrame bounds one frame: a snapshot of thousands of connections is
// under a megabyte.
const maxFrame = 16 << 20

// The operations.
const (
	opStatus           = "status"
	opWatchGroups      = "watch-groups"
	opWatchTraffic     = "watch-traffic"
	opWatchConnections = "watch-connections"
	opSelectOutbound   = "select-outbound"
	opURLTest          = "url-test"
	opMode             = "mode"
	opSetMode          = "set-mode"
	opCloseConnection  = "close-connection"
	opCloseAll         = "close-all"
	opLogs             = "logs"
	opExit             = "exit"
)

// request is what a call asks.
type request struct {
	Op         string `json:"op"`
	Group      string `json:"group,omitempty"`
	Outbound   string `json:"outbound,omitempty"`
	Tag        string `json:"tag,omitempty"`
	Mode       string `json:"mode,omitempty"`
	ID         string `json:"id,omitempty"`
	IntervalMS int64  `json:"interval_ms,omitempty"`
}

// answer is what the engine said. Err is empty on success; Kind names one of
// the engine's sentinel errors, so that the application's side tells them
// apart as the controller expects.
type answer struct {
	Err  string `json:"err,omitempty"`
	Kind string `json:"kind,omitempty"`

	// status
	Running bool   `json:"running,omitempty"`
	Tun     string `json:"tun,omitempty"`
	Version string `json:"version,omitempty"`
	// mode
	Mode  string   `json:"mode,omitempty"`
	Modes []string `json:"modes,omitempty"`
	// exit: the core stopped by itself, why, and whether the system ended
	// the VPN (engine.ExitEvent)
	Cause   string `json:"cause,omitempty"`
	Revoked bool   `json:"revoked,omitempty"`
}

// LogLine is one line of the core's log.
type LogLine struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

func errorKind(err error) string {
	switch {
	case errors.Is(err, engine.ErrNotRunning):
		return "not-running"
	case errors.Is(err, engine.ErrNotFound):
		return "not-found"
	case errors.Is(err, engine.ErrInvalid):
		return "invalid"
	}
	return ""
}

func (a answer) error() error {
	if a.Err == "" {
		return nil
	}
	switch a.Kind {
	case "not-running":
		return fmt.Errorf("%w: %s", engine.ErrNotRunning, a.Err)
	case "not-found":
		return fmt.Errorf("%w: %s", engine.ErrNotFound, a.Err)
	case "invalid":
		return fmt.Errorf("%w: %s", engine.ErrInvalid, a.Err)
	}
	return errors.New(a.Err)
}

func writeFrame(w io.Writer, b []byte) error {
	if len(b) > maxFrame {
		return fmt.Errorf("remote: a frame of %d bytes is too large", len(b))
	}
	var head [4]byte
	binary.BigEndian.PutUint32(head[:], uint32(len(b)))
	if _, err := w.Write(head[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

func readFrame(r io.Reader) ([]byte, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(head[:])
	if n > maxFrame {
		return nil, fmt.Errorf("remote: a frame of %d bytes is too large", n)
	}
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return b, err
}

func writeJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeFrame(w, b)
}

func readJSON(r io.Reader, v any) error {
	b, err := readFrame(r)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
