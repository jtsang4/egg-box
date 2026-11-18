// The packet tunnel provider of EggBox on iOS: the system starts it with the
// VPN, in a process of its own. The core is Go (cmd/eggbox-tunnel, linked as
// libeggboxcore.a): it reads the configuration the application's agent left
// in the App Group container, runs it, and serves the application's calls on
// a socket there. This side applies the tunnel's network settings the core
// asks for, tells it the device's default network, and ends the tunnel when
// the core stops by itself.

import Foundation
import Network
import NetworkExtension

struct TunnelError: LocalizedError {
    let message: String
    init(_ message: String) { self.message = message }
    var errorDescription: String? { message }
}

final class PacketTunnelProvider: NEPacketTunnelProvider {
    private var monitor: NWPathMonitor?

    override func startTunnel(options: [String: NSObject]?, completionHandler: @escaping (Error?) -> Void) {
        guard let group = Bundle.main.object(forInfoDictionaryKey: "EggBoxAppGroup") as? String,
              let dir = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: group)
        else {
            completionHandler(TunnelError("EggBox's shared container is missing: the extension needs the App Group of the application"))
            return
        }
        startMonitor()
        // The provider, as the core's callbacks find it: an address, which is
        // Sendable, rather than the pointer, which is not.
        let ctx = UInt(bitPattern: Unmanaged.passUnretained(self).toOpaque())
        DispatchQueue.global(qos: .userInitiated).async {
            let failure = dir.path.withCString { path in
                EggboxTunnelStart(UnsafeMutablePointer(mutating: path), eggboxApply, eggboxCancel, UnsafeMutableRawPointer(bitPattern: ctx))
            }
            if let failure {
                let message = String(cString: failure)
                free(failure)
                completionHandler(TunnelError(message))
            } else {
                completionHandler(nil)
            }
        }
    }

    override func stopTunnel(with reason: NEProviderStopReason, completionHandler: @escaping () -> Void) {
        monitor?.cancel()
        monitor = nil
        DispatchQueue.global(qos: .userInitiated).async {
            EggboxTunnelStop()
            completionHandler()
        }
    }

    // The device's default network, which the core sends its own traffic
    // through: the provider's sockets never enter its tunnel.
    private func startMonitor() {
        let m = NWPathMonitor()
        m.pathUpdateHandler = { path in
            let uplink = path.availableInterfaces.first { $0.type == .wifi || $0.type == .cellular || $0.type == .wiredEthernet }
            guard path.status == .satisfied, let uplink else {
                "".withCString { EggboxTunnelSetDefault(UnsafeMutablePointer(mutating: $0), -1, 0, 0) }
                return
            }
            uplink.name.withCString {
                EggboxTunnelSetDefault(UnsafeMutablePointer(mutating: $0), Int32(uplink.index),
                                       path.isExpensive ? 1 : 0, path.isConstrained ? 1 : 0)
            }
        }
        m.start(queue: DispatchQueue(label: "org.eggbox.tunnel.path"))
        monitor = m
    }

    // Settings are the core's (iostunnel.Settings).
    struct Settings: Decodable {
        struct Prefix: Decodable {
            let address: String
            let bits: Int
            let ipv6: Bool
            let mask: String?
        }
        let addresses: [Prefix]
        let routes: [Prefix]?
        let excluded_routes: [Prefix]?
        let dns: [String]?
        let mtu: Int
    }

    // apply sets the tunnel's network settings and waits for the system.
    // It runs on a thread of the core's, never the provider's queue.
    func apply(_ json: String) -> String? {
        let s: Settings
        do {
            s = try JSONDecoder().decode(Settings.self, from: Data(json.utf8))
        } catch {
            return "the tunnel's settings cannot be read: \(error.localizedDescription)"
        }
        let n = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: "127.0.0.1")
        n.mtu = NSNumber(value: s.mtu)
        let v4 = s.addresses.filter { !$0.ipv6 }
        if !v4.isEmpty {
            let ip = NEIPv4Settings(addresses: v4.map(\.address), subnetMasks: v4.map { $0.mask ?? "255.255.255.255" })
            ip.includedRoutes = (s.routes ?? []).filter { !$0.ipv6 }.map {
                $0.bits == 0 ? NEIPv4Route.default() : NEIPv4Route(destinationAddress: $0.address, subnetMask: $0.mask ?? "255.255.255.255")
            }
            ip.excludedRoutes = (s.excluded_routes ?? []).filter { !$0.ipv6 }.map {
                NEIPv4Route(destinationAddress: $0.address, subnetMask: $0.mask ?? "255.255.255.255")
            }
            n.ipv4Settings = ip
        }
        let v6 = s.addresses.filter(\.ipv6)
        if !v6.isEmpty {
            let ip = NEIPv6Settings(addresses: v6.map(\.address), networkPrefixLengths: v6.map { NSNumber(value: $0.bits) })
            ip.includedRoutes = (s.routes ?? []).filter(\.ipv6).map {
                $0.bits == 0 ? NEIPv6Route.default() : NEIPv6Route(destinationAddress: $0.address, networkPrefixLength: NSNumber(value: $0.bits))
            }
            ip.excludedRoutes = (s.excluded_routes ?? []).filter(\.ipv6).map {
                NEIPv6Route(destinationAddress: $0.address, networkPrefixLength: NSNumber(value: $0.bits))
            }
            n.ipv6Settings = ip
        }
        if let dns = s.dns, !dns.isEmpty {
            let d = NEDNSSettings(servers: dns)
            d.matchDomains = [""] // every name: the core answers them
            n.dnsSettings = d
        }
        let done = DispatchSemaphore(value: 0)
        var failure: String?
        setTunnelNetworkSettings(n) { error in
            failure = error?.localizedDescription
            done.signal()
        }
        if done.wait(timeout: .now() + 30) == .timedOut {
            return "the system did not apply the tunnel's settings in time"
        }
        return failure
    }
}

// The functions the core calls, as C function pointers: they capture
// nothing, and find the provider through the context they are given.

private let eggboxApply: eggbox_apply_fn = { ctx, settings in
    guard let ctx, let settings else { return strdup("no settings") }
    let provider = Unmanaged<PacketTunnelProvider>.fromOpaque(ctx).takeUnretainedValue()
    if let failure = provider.apply(String(cString: settings)) {
        return strdup(failure)
    }
    return nil
}

private let eggboxCancel: eggbox_cancel_fn = { ctx, reason in
    guard let ctx else { return }
    let provider = Unmanaged<PacketTunnelProvider>.fromOpaque(ctx).takeUnretainedValue()
    provider.cancelTunnelWithError(TunnelError(reason.map { String(cString: $0) } ?? "the core stopped"))
}
