package winplat

import "github.com/jtsang4/sing-box-ui/internal/agent/platform"

// The power notifications Windows sends a service (PBT_* values).
const (
	pbtResumeCritical  uint32 = 0x6
	pbtResumeSuspend   uint32 = 0x7
	pbtSuspend         uint32 = 0x4
	pbtResumeAutomatic uint32 = 0x12
)

// PowerEvent turns a power notification into what the agent understands. The
// second result is false for notifications that matter to nobody here, such
// as a change of battery level.
func PowerEvent(eventType uint32) (platform.EventKind, bool) {
	switch eventType {
	case pbtSuspend:
		return platform.Slept, true
	case pbtResumeAutomatic, pbtResumeSuspend, pbtResumeCritical:
		return platform.Woke, true
	}
	return 0, false
}
