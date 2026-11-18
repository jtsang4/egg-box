import Foundation

// The plugin only declares what the application needs of the system (see
// mygo-plugin.json); the VPN is the iosvpn native module.
final class EggBoxVPNPlugin: MyGoPlugin {
    func call(_ method: String, _ args: [String: Any], _ reply: @escaping PluginReply) {
        reply(.failure(PluginFailure("NoMethod", method)))
    }
}
