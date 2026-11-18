// Package iosplugin is what EggBox's iOS application declares to the system,
// as a mobile plugin of MyGo (mygo-plugin.json): it manages a packet tunnel
// (the NetworkExtension framework, the Network Extensions capability) and
// shares an App Group container with the extension that runs the core,
// whose name its Info.plist carries for both. A simulator's build is signed
// with these entitlements; a device's comes from its provisioning profile,
// which must have both capabilities.
//
// Importing the package is all it takes (cmd/egg-box does on iOS). Its Swift
// answers no call: the VPN itself is the iosvpn native module.
package iosplugin
