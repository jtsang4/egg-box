package urltestgroup

import (
	"errors"
	"fmt"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common"
)

// ErrUnknownOutbound means the configuration has no outbound of that tag.
var ErrUnknownOutbound = errors.New("outbound not found")

// ErrNoGroup means the configuration has no group of this kind, so there is
// no history store to record a test in.
var ErrNoGroup = errors.New("no URL test group is running")

// Test measures tag in the background, as sing-box's daemon does for its
// URLTest call: a group of this kind checks its members, another group has
// all of its members (and theirs) measured, and a single outbound is
// measured alone. Results go to the history store the group list reads.
func Test(manager adapter.OutboundManager, tag string) error {
	outbound, ok := manager.Outbound(tag)
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownOutbound, tag)
	}
	// Any group of this kind carries the history store, the core's context
	// and the configured test link (all groups get the same one).
	var ref *URLTest
	for _, o := range manager.Outbounds() {
		if g, ok := o.(*URLTest); ok && g.group != nil {
			ref = g
			break
		}
	}
	if ref == nil {
		return ErrNoGroup
	}
	history, ctx, logger, link := ref.group.history, ref.ctx, ref.logger, ref.link
	switch target := outbound.(type) {
	case *URLTest:
		go target.CheckOutbounds()
	case adapter.OutboundGroup:
		members := common.FilterNotNil(common.Map(target.All(), func(it string) adapter.Outbound {
			member, _ := manager.Outbound(it)
			return member
		}))
		go URLTestOutbounds(ctx, manager, history, logger, members, link, 0, true)
	default:
		go func() {
			delay, err := Measure(ctx, link, outbound)
			if err != nil {
				history.DeleteURLTestHistory(tag)
				return
			}
			history.StoreURLTestHistory(tag, &adapter.URLTestHistory{Time: time.Now(), Delay: delay})
		}()
	}
	return nil
}
