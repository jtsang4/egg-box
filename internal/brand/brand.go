// Package brand centralises every identifier that has to match across the GUI,
// the privileged agent, the installers and the platform service registrations.
// Changing a value here is a coordinated change: installers, the macOS
// LaunchDaemon plist and the Swift bridge all embed the same strings.
package brand

const (
	// DisplayName is the product name shown to people.
	DisplayName = "Sing-Box UI"

	// AppID is the reverse-DNS application identifier (bundle id on macOS).
	AppID = "me.jtsang.singboxui"

	// AgentLabel is the LaunchDaemon label on macOS. It doubles as the Mach
	// service name the XPC listener registers under.
	AgentLabel = "me.jtsang.singboxui.agent"

	// AgentBinary is the file name of the privileged agent executable.
	AgentBinary = "agentd"

	// WindowsServiceName is the Service Control Manager name of the agent.
	WindowsServiceName = "SingBoxUIAgent"

	// WindowsServiceDisplayName is what services.msc shows.
	WindowsServiceDisplayName = "Sing-Box UI Agent"

	// WindowsServiceDescription explains the service to administrators.
	WindowsServiceDescription = "Runs the Sing-Box UI network core with the privileges needed to manage a TUN adapter, routes and DNS."

	// WindowsPipePath is the named pipe the GUI connects to.
	WindowsPipePath = `\\.\pipe\` + AgentLabel

	// TunInterfaceName is the Wintun adapter name on Windows. macOS interface
	// names are assigned by the kernel (utunN) and must never be assumed.
	TunInterfaceName = "SingBoxUI"

	// ProtocolVersion is bumped whenever the GUI<->agent contract changes in a
	// way an older peer cannot understand. The handshake rejects mismatches.
	ProtocolVersion = 1
)

// Directory names used beneath the platform specific roots.
const (
	// DataDirName is the folder under ProgramData (Windows) or
	// /Library/Application Support (macOS) owned by the agent.
	DataDirName = "SingBoxUI"

	// UserDirName is the folder under the per-user config directory used by the GUI.
	UserDirName = "SingBoxUI"
)
