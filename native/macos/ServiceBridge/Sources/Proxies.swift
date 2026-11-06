// The system proxy settings, read only.
//
// Programs that use the system proxy send their traffic to it before it
// reaches the tunnel, so a proxy another program left behind cuts them off.
// The application reads the settings to tell the person; it never changes
// them. Reading needs no privileges.

import Foundation
import SystemConfiguration

/// The keys of a service's proxy settings the application looks at. Nothing
/// else is copied out (exception lists can be long and are not needed).
private let proxyKeys = [
    "HTTPEnable", "HTTPProxy", "HTTPPort",
    "HTTPSEnable", "HTTPSProxy", "HTTPSPort",
    "SOCKSEnable", "SOCKSProxy", "SOCKSPort",
]

/// Writes, as JSON, the enabled services of the current network set with their
/// manual proxy settings and the identifier of the service that carries
/// traffic now:
///
///     {"primary": "<id>", "services": [{"id", "name", "proxies": {...}}]}
///
/// The result is the length written, or -1 when the settings cannot be read,
/// or minus the length needed when `out` is too small.
@_cdecl("eggbox_system_proxies")
public func eggbox_system_proxies(_ out: UnsafeMutablePointer<CChar>?, _ capacity: Int32) -> Int32 {
    guard let prefs = SCPreferencesCreate(nil, "org.eggbox.app" as CFString, nil),
          let set = SCNetworkSetCopyCurrent(prefs),
          let services = SCNetworkSetCopyServices(set) as? [SCNetworkService]
    else { return -1 }

    var list: [[String: Any]] = []
    for service in services where SCNetworkServiceGetEnabled(service) {
        guard let id = SCNetworkServiceGetServiceID(service) as String? else { continue }
        var proxies: [String: Any] = [:]
        if let proto = SCNetworkServiceCopyProtocol(service, kSCNetworkProtocolTypeProxies),
           SCNetworkProtocolGetEnabled(proto),
           let config = SCNetworkProtocolGetConfiguration(proto) as? [String: Any] {
            for key in proxyKeys {
                if let v = config[key] as? String {
                    proxies[key] = v
                } else if let v = config[key] as? NSNumber {
                    proxies[key] = v.intValue
                }
            }
        }
        list.append([
            "id": id,
            "name": (SCNetworkServiceGetName(service) as String?) ?? id,
            "proxies": proxies,
        ])
    }

    var primary = ""
    if let store = SCDynamicStoreCreate(nil, "org.eggbox.app" as CFString, nil, nil),
       let global = SCDynamicStoreCopyValue(store, "State:/Network/Global/IPv4" as CFString) as? [String: Any],
       let id = global["PrimaryService"] as? String {
        primary = id
    }

    guard let data = try? JSONSerialization.data(withJSONObject: ["primary": primary, "services": list]) else { return -1 }
    guard let out, data.count < Int(capacity) else { return -Int32(data.count + 1) }
    data.withUnsafeBytes { raw in
        for (i, b) in raw.enumerated() { out[i] = CChar(bitPattern: b) }
    }
    out[data.count] = 0
    return Int32(data.count)
}
