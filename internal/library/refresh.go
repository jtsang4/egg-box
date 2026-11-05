package library

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"eggbox/internal/subscription"
)

// RefreshOptions tune a refresh.
type RefreshOptions struct {
	// Force ignores the validators of the last download.
	Force bool
	// Client replaces the default HTTP client (tests).
	Client *http.Client
}

// RefreshResult says what a refresh found.
type RefreshResult struct {
	// NotModified: the provider confirmed nothing changed.
	NotModified bool
	// Changed: the set of nodes differs from before.
	Changed        bool
	Added, Removed int
	NodeCount      int
	Skipped        []subscription.Skipped
}

// Refresh downloads a remote subscription and replaces its nodes. Failures
// are remembered on the subscription and leave the previous nodes in place.
func (l *Library) Refresh(ctx context.Context, id string, opts RefreshOptions) (*RefreshResult, error) {
	sub, ok := l.Subscription(id)
	if !ok {
		return nil, ErrNotFound
	}
	if sub.Kind != KindRemote {
		return nil, errors.New("this subscription has no link to refresh from")
	}
	link, err := l.secrets.Get(secretKey(id))
	if err != nil {
		err = fmt.Errorf("the subscription link is not available: %w", err)
		l.recordFailure(id, err)
		return nil, err
	}
	fo := subscription.FetchOptions{UserAgent: sub.UserAgent, Client: opts.Client}
	if !opts.Force {
		fo.ETag, fo.LastModified = sub.ETag, sub.LastModified
	}
	fetched, err := subscription.Fetch(ctx, link, fo)
	if err != nil {
		l.recordFailure(id, err)
		return nil, err
	}
	now := l.now().UTC()

	if fetched.NotModified {
		err := l.mutate(func() error {
			s := l.find(id)
			if s == nil {
				return ErrNotFound
			}
			s.CheckedAt, s.LastError = now, ""
			if fetched.Usage != nil {
				u := *fetched.Usage
				s.Usage = &u
			}
			return nil
		})
		return &RefreshResult{NotModified: true, NodeCount: sub.NodeCount}, err
	}

	res, err := subscription.Parse(fetched.Body)
	if err != nil {
		l.recordFailure(id, err)
		return nil, err
	}

	old, _ := l.Nodes(id)
	added, removed := diffNodes(old, res.Nodes)
	if err := l.writeNodes(id, res.Nodes); err != nil {
		l.recordFailure(id, err)
		return nil, err
	}
	err = l.mutate(func() error {
		s := l.find(id)
		if s == nil {
			return ErrNotFound
		}
		applyResult(s, res, now)
		s.ETag, s.LastModified = fetched.ETag, fetched.LastModified
		s.Usage = nil
		if fetched.Usage != nil {
			u := *fetched.Usage
			s.Usage = &u
		}
		if fetched.HomePage != "" {
			s.HomePage = fetched.HomePage
		}
		if fetched.Title != "" && (s.Name == "" || s.Name == s.Host) {
			s.Name = fetched.Title
		}
		if s.IntervalAuto && fetched.Interval > 0 {
			s.IntervalHours = max(1, int((fetched.Interval+30*time.Minute)/time.Hour))
		}
		l.nodes[id] = res.Nodes
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &RefreshResult{
		Changed: added+removed > 0, Added: added, Removed: removed,
		NodeCount: len(res.Nodes), Skipped: res.Skipped,
	}, nil
}

func applyResult(s *Subscription, res *subscription.Result, now time.Time) {
	s.UpdatedAt, s.CheckedAt, s.LastError = now, now, ""
	s.NodeCount = len(res.Nodes)
	s.SkippedCount = 0
	for _, sk := range res.Skipped {
		if sk.Code != subscription.SkipInfoEntry && sk.Code != subscription.SkipDuplicate {
			s.SkippedCount++
		}
	}
	s.Format = string(res.Format)
	s.Notices = res.Notices
}

func (l *Library) recordFailure(id string, cause error) {
	msg := strings.TrimSpace(cause.Error())
	if msg == "" {
		msg = "the update failed"
	}
	now := l.now().UTC()
	_ = l.mutate(func() error {
		s := l.find(id)
		if s == nil {
			return ErrNotFound
		}
		s.CheckedAt, s.LastError = now, msg
		return nil
	})
}

func diffNodes(old, cur []subscription.Node) (added, removed int) {
	was := map[string]bool{}
	for _, n := range old {
		was[n.Key()] = true
	}
	is := map[string]bool{}
	for _, n := range cur {
		k := n.Key()
		is[k] = true
		if !was[k] {
			added++
		}
	}
	for k := range was {
		if !is[k] {
			removed++
		}
	}
	return added, removed
}
