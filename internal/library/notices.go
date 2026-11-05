package library

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"maps"
	"time"
)

// Notice is something the person should be told once: the interface shows it
// when it next can and then dismisses it. Notices are kept in the library so
// that a migration or a refresh done while no window was open is not lost.
type Notice struct {
	ID   string            `json:"id"`
	Time time.Time         `json:"time"`
	Code string            `json:"code"`
	Args map[string]string `json:"args,omitempty"`
}

// maxPendingNotices bounds the notices kept for a window that never opens.
const maxPendingNotices = 50

func (l *Library) addNoticeLocked(code string, args map[string]string) {
	for _, n := range l.data.Notices {
		if n.Code == code && maps.Equal(n.Args, args) {
			return
		}
	}
	var b [5]byte
	_, _ = rand.Read(b[:])
	l.data.Notices = append(l.data.Notices, Notice{
		ID: "n" + hex.EncodeToString(b[:]), Time: l.now().UTC(), Code: code, Args: maps.Clone(args),
	})
	if n := len(l.data.Notices); n > maxPendingNotices {
		l.data.Notices = append([]Notice(nil), l.data.Notices[n-maxPendingNotices:]...)
	}
}

// AddNotice queues a notice. The same notice (code and arguments) is not
// queued twice while the first one waits.
func (l *Library) AddNotice(code string, args map[string]string) error {
	return l.mutate(func() error {
		l.addNoticeLocked(code, args)
		return nil
	})
}

// PendingNotices lists the notices not shown yet, oldest first.
func (l *Library) PendingNotices() []Notice {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Notice, len(l.data.Notices))
	for i, n := range l.data.Notices {
		out[i] = n
		out[i].Args = maps.Clone(n.Args)
	}
	return out
}

// ErrNoNotice means no pending notice has the id.
var ErrNoNotice = errors.New("no such notice")

// DismissNotice removes a notice that was shown.
func (l *Library) DismissNotice(id string) error {
	return l.mutate(func() error {
		for i, n := range l.data.Notices {
			if n.ID == id {
				l.data.Notices = append(l.data.Notices[:i:i], l.data.Notices[i+1:]...)
				return nil
			}
		}
		return ErrNoNotice
	})
}
