package singbox

import "eggbox/internal/agent/policy"

// PolicyOptions returns the policy options that match what this build of the
// core can actually run, so a configuration is never accepted for a protocol
// whose code was compiled out.
func PolicyOptions(reg RegistryOptions) policy.Options {
	return policy.Options{
		AllowServerInbounds: reg.ServerInbounds,
		WireGuard:           WireGuardCompiled,
		QUIC:                QUICCompiled,
		DHCP:                DHCPCompiled,
		UTLS:                UTLSCompiled,
	}
}
