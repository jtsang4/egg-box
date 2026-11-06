package app

import (
	"context"
	"time"

	"eggbox/internal/configgen"
	"eggbox/internal/sysproxy"
)

// How often the system proxy settings are looked at while connected. Nothing
// tells the application when they change, and reading them is cheap.
const (
	defaultProxyCheckEvery = 30 * time.Second
	proxyPhaseLook         = 2 * time.Second
)

// watchSystemProxy looks at the system's proxy settings while connected, so
// that a proxy left behind by another program - which cuts off every program
// that uses the system proxy, while everything else works - is found and
// shown. It looks as soon as the connection is up and then every so often;
// when the connection goes down what was found is cleared, since it is about
// the connection the person is using.
func (s *Service) watchSystemProxy(ctx context.Context) {
	read := s.cfg.SystemProxies
	if read == nil {
		return
	}
	probe := s.cfg.ProbeProxy
	if probe == nil {
		probe = sysproxy.Probe
	}
	every := s.cfg.ProxyCheckEvery
	if every <= 0 {
		every = defaultProxyCheckEvery
	}
	tick := time.NewTicker(min(every, proxyPhaseLook))
	defer tick.Stop()

	var (
		wasOnline bool
		last      time.Time
		// warned holds the addresses told about this session.
		warned = map[string]bool{}
	)
	for {
		online := s.Snapshot().Phase.Online()
		switch {
		case online && (!wasOnline || time.Since(last) >= every):
			last = time.Now()
			s.checkSystemProxy(ctx, read, probe, warned)
		case !online && wasOnline:
			s.update(func(sn *Snapshot) { sn.ProxyIssues = nil })
		}
		wasOnline = online
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Service) checkSystemProxy(ctx context.Context, read func() ([]sysproxy.Setting, error), probe sysproxy.Listening, warned map[string]bool) {
	settings, err := read()
	if err != nil {
		// Not knowing is no reason to bother anybody; keep what was found.
		return
	}
	issues := sysproxy.Analyze(ctx, settings, ownProxyPorts(s.lib.Prefs()), probe)
	s.update(func(sn *Snapshot) { sn.ProxyIssues = issues })
	for _, is := range issues {
		if is.State == sysproxy.Dead && is.Active && !warned[is.Endpoint()] {
			warned[is.Endpoint()] = true
			s.notice(LevelWarning, "system_proxy_dead", "", map[string]string{"endpoint": is.Endpoint()})
		}
	}
}

// ownProxyPorts are the local ports this application's proxy listens on: a
// system proxy pointing there is the person's choice.
func ownProxyPorts(p configgen.Prefs) []int {
	switch {
	case p.MixedPort > 0:
		return []int{p.MixedPort}
	case p.TunDisabled:
		return []int{configgen.DefaultMixedPort}
	}
	return nil
}
