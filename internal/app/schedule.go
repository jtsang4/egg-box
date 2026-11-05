package app

import (
	"context"
	"time"
)

// schedule refreshes subscriptions and rule data when they are due.
func (s *Service) schedule(ctx context.Context) {
	first := time.NewTimer(s.cfg.ScheduleDelay)
	defer first.Stop()
	t := time.NewTicker(s.cfg.ScheduleEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
		case <-t.C:
		}
		s.tick(ctx)
	}
}

// tick does one round of scheduled work.
func (s *Service) tick(ctx context.Context) {
	settings := s.lib.Settings()
	if settings.AutoRefresh {
		changed := false
		for _, id := range s.lib.Due() {
			if ctx.Err() != nil {
				return
			}
			rctx, cancel := context.WithTimeout(ctx, 90*time.Second)
			res, err := s.refresh(rctx, id)
			cancel()
			if err != nil {
				continue // the failure is on the subscription; Due retries it later
			}
			changed = changed || res.Changed
		}
		if changed {
			s.notice(LevelInfo, "subscriptions_auto_updated", "", nil)
			rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			s.reapplyIfRefreshed(rctx)
			cancel()
		}
	}
	if settings.RuleUpdateHours > 0 && s.ruleDataStale(time.Duration(settings.RuleUpdateHours)*time.Hour) {
		rctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		s.updateScheduledRuleData(rctx, time.Duration(settings.RuleUpdateHours)*time.Hour)
		cancel()
	}
}
