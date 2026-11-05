package ledger

import (
	"context"
	"errors"
	"fmt"
)

// Outcome is the result of releasing one recorded resource.
type Outcome int

const (
	// Released means the resource existed, was ours and has been removed.
	Released Outcome = iota + 1
	// AlreadyGone means there was nothing left to remove.
	AlreadyGone
	// NotOurs means the live resource no longer carries our fingerprint
	// (somebody else replaced it). It is left untouched and forgotten.
	NotOurs
)

func (o Outcome) String() string {
	switch o {
	case Released:
		return "released"
	case AlreadyGone:
		return "already-gone"
	case NotOurs:
		return "not-ours"
	default:
		return "unknown"
	}
}

// Releaser removes one recorded resource after verifying it is still ours.
type Releaser interface {
	Release(ctx context.Context, e Entry) (Outcome, error)
}

// Failure pairs an entry with the reason it could not be released.
type Failure struct {
	Entry Entry
	Err   error
}

// Report summarises a cleanup pass.
type Report struct {
	Released []Entry
	Gone     []Entry
	NotOurs  []Entry
	Failed   []Failure
}

// OK reports whether every recorded resource has been dealt with.
func (r Report) OK() bool { return len(r.Failed) == 0 }

// Total is the number of entries the pass looked at.
func (r Report) Total() int {
	return len(r.Released) + len(r.Gone) + len(r.NotOurs) + len(r.Failed)
}

// Summary is a short human readable description, used for status text.
func (r Report) Summary() string {
	if r.Total() == 0 {
		return "nothing to clean"
	}
	s := fmt.Sprintf("released %d, already gone %d, not ours %d", len(r.Released), len(r.Gone), len(r.NotOurs))
	if len(r.Failed) > 0 {
		s += fmt.Sprintf(", failed %d (%v)", len(r.Failed), r.Failed[0].Err)
	}
	return s
}

// Cleanup releases every recorded resource, newest first. Resolved entries are
// removed from the ledger as it goes; failures stay recorded so the next
// attempt (or an operator) can still see and retry them. When everything is
// resolved the ledger is marked clean.
func (l *Ledger) Cleanup(ctx context.Context, r Releaser) Report {
	var rep Report
	for _, e := range l.Entries() {
		if err := ctx.Err(); err != nil {
			rep.Failed = append(rep.Failed, Failure{Entry: e, Err: err})
			continue
		}
		out, err := r.Release(ctx, e)
		if err != nil {
			rep.Failed = append(rep.Failed, Failure{Entry: e, Err: err})
			continue
		}
		switch out {
		case Released:
			rep.Released = append(rep.Released, e)
		case AlreadyGone:
			rep.Gone = append(rep.Gone, e)
		case NotOurs:
			rep.NotOurs = append(rep.NotOurs, e)
		default:
			rep.Failed = append(rep.Failed, Failure{Entry: e, Err: errors.New("releaser returned an unknown outcome")})
			continue
		}
		if err := l.Remove(e.ID); err != nil {
			rep.Failed = append(rep.Failed, Failure{Entry: e, Err: fmt.Errorf("update ledger: %w", err)})
		}
	}
	if rep.OK() {
		if err := l.MarkClean(); err != nil {
			rep.Failed = append(rep.Failed, Failure{Err: fmt.Errorf("mark clean: %w", err)})
		}
	}
	return rep
}
