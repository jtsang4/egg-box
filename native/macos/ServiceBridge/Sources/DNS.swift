// The temporary system resolver.
//
// While the tunnel is up the system's own DNS has to ask the tunnel, or names
// would be looked up outside it. The way the system lets a program do that
// without touching the person's network settings is a resolver entry in the
// dynamic store (what `scutil` edits): a "service" that has only DNS in it and
// that matches every domain. It is state, not configuration: it does not
// survive a restart, and nothing in System Settings changes.
//
// The entry carries a token only this program knows, so that taking it down
// can tell its own entry from one somebody else wrote under the same name.

import Foundation
import SystemConfiguration

private let tokenKey = "EggBoxToken"

private func openStore() -> SCDynamicStore? {
    SCDynamicStoreCreate(nil, "org.eggbox.app" as CFString, nil, nil)
}

private func describe(_ fallback: String) -> String {
    let code = SCError()
    if code == 0 { return fallback }
    return "\(fallback): \(String(cString: SCErrorString(code)))"
}

/// Puts the resolver entry up: the system asks `servers` (comma separated
/// addresses) for every domain. Needs to run as root.
@_cdecl("eggbox_dns_set")
public func eggbox_dns_set(
    _ key: UnsafePointer<CChar>, _ servers: UnsafePointer<CChar>, _ token: UnsafePointer<CChar>,
    _ err: UnsafeMutablePointer<CChar>?, _ cap: Int32
) -> Int32 {
    let out = ErrorOut(buffer: err, capacity: cap)
    guard let store = openStore() else {
        out.set(describe("cannot open the system configuration"))
        return Code.failed
    }
    let addresses = String(cString: servers).split(separator: ",").map { String($0).trimmingCharacters(in: .whitespaces) }
    let value: [String: Any] = [
        "ServerAddresses": addresses,
        // The empty domain matches every name.
        "SupplementalMatchDomains": [""],
        // Ahead of every other resolver for every name, including another
        // VPN's catch-all one (those get orders from 100000 up), so a takeover
        // of the routes takes the names with it. More specific match domains
        // still win over the empty one whatever the order. configd drops
        // "SearchOrder" from a service that has no IPv4 or IPv6, which this
        // entry never has, so the order goes per domain here, one per entry
        // of SupplementalMatchDomains (a count that differs drops the entry).
        "SupplementalMatchOrders": [1],
        tokenKey: String(cString: token),
    ]
    if !SCDynamicStoreSetValue(store, String(cString: key) as CFString, value as CFDictionary) {
        out.set(describe("cannot set the resolver"))
        return Code.failed
    }
    return Code.ok
}

/// Reads the token of the entry under the key into `out`. The result is its
/// length, 0 for an entry that carries no token of ours, and -1 when there is
/// no entry.
@_cdecl("eggbox_dns_token")
public func eggbox_dns_token(_ key: UnsafePointer<CChar>, _ out: UnsafeMutablePointer<CChar>?, _ cap: Int32) -> Int32 {
    guard let store = openStore(),
          let value = SCDynamicStoreCopyValue(store, String(cString: key) as CFString) as? [String: Any] else { return -1 }
    let token = (value[tokenKey] as? String) ?? ""
    ErrorOut(buffer: out, capacity: cap).set(token)
    return Int32(min(token.utf8.count, Int(max(cap - 1, 0))))
}

/// Takes the entry down. There being none is not a failure.
@_cdecl("eggbox_dns_remove")
public func eggbox_dns_remove(_ key: UnsafePointer<CChar>, _ err: UnsafeMutablePointer<CChar>?, _ cap: Int32) -> Int32 {
    let out = ErrorOut(buffer: err, capacity: cap)
    guard let store = openStore() else {
        out.set(describe("cannot open the system configuration"))
        return Code.failed
    }
    if SCDynamicStoreCopyValue(store, String(cString: key) as CFString) == nil { return Code.ok }
    if !SCDynamicStoreRemoveValue(store, String(cString: key) as CFString) {
        out.set(describe("cannot remove the resolver"))
        return Code.failed
    }
    return Code.ok
}
