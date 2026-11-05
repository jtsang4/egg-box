package daemon

import (
	"fmt"
	"os"
	"sync"
)

// rotatingFile is a size-capped log file: when it would grow past max bytes it
// becomes name.1, name.1 becomes name.2, and so on, keeping the newest keep
// files. The agent runs for months, so its log must never grow without bound.
type rotatingFile struct {
	mu   sync.Mutex
	path string
	max  int64
	keep int
	f    *os.File
	size int64
}

func openRotating(path string, max int64, keep int) (*rotatingFile, error) {
	r := &rotatingFile{path: path, max: max, keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return 0, os.ErrClosed
	}
	if r.size > 0 && r.size+int64(len(p)) > r.max {
		r.rotateLocked()
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// rotateLocked shifts the files. A failure to rename is not worth losing log
// lines over: it keeps appending to the current file.
func (r *rotatingFile) rotateLocked() {
	_ = r.f.Close()
	r.f = nil
	_ = os.Remove(fmt.Sprintf("%s.%d", r.path, r.keep))
	for i := r.keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
	}
	_ = os.Rename(r.path, r.path+".1")
	if err := r.open(); err != nil {
		// Could not reopen: fall back to appending to whatever exists, or give up quietly.
		r.f, r.size = nil, 0
		if f, e := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); e == nil {
			r.f = f
		}
	}
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
