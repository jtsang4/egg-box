import Foundation
import NetworkExtension

// iOS's VPN for EggBox: the system's manager of the application's packet
// tunnel, whose extension is the application's identifier plus ".tunnel".

struct TunnelFailure: LocalizedError {
    let message: String
    init(_ message: String) { self.message = message }
    var errorDescription: String? { message }
}

func makeTunnel() throws -> any Tunnel { SystemTunnel() }

final class SystemTunnel: Tunnel {
    private var appGroup: String { Bundle.main.object(forInfoDictionaryKey: "EggBoxAppGroup") as? String ?? "" }
    private var providerID: String { (Bundle.main.bundleIdentifier ?? "org.eggbox.app") + ".tunnel" }

    func groupDirectory() throws -> String {
        guard !appGroup.isEmpty,
              let url = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: appGroup)
        else {
            throw TunnelFailure("EggBox's shared container is missing: the application needs its App Group")
        }
        return url.path
    }

    private func ours(_ m: NETunnelProviderManager) -> Bool {
        (m.protocolConfiguration as? NETunnelProviderProtocol)?.providerBundleIdentifier == providerID
    }

    /// The installed configuration, if any.
    private func installed() async throws -> NETunnelProviderManager? {
        try await NETunnelProviderManager.loadAllFromPreferences().first(where: ours)
    }

    /// The configuration, installed the first time: the system asks the
    /// person to allow it.
    private func manager() async throws -> NETunnelProviderManager {
        if let m = try await installed() {
            if !m.isEnabled {
                m.isEnabled = true
                try await m.saveToPreferences()
                try await m.loadFromPreferences()
            }
            return m
        }
        let p = NETunnelProviderProtocol()
        p.providerBundleIdentifier = providerID
        p.serverAddress = "EggBox"
        let m = NETunnelProviderManager()
        m.protocolConfiguration = p
        m.localizedDescription = "EggBox"
        m.isEnabled = true
        do {
            try await m.saveToPreferences()
        } catch let e as NEVPNError where e.code == .configurationReadWriteFailed {
            // The person chose Don't Allow when the system asked.
            throw IosvpnError.notAllowed
        }
        try await m.loadFromPreferences()
        return m
    }

    func install() async throws {
        _ = try await manager()
    }

    func start() async throws {
        let session = try await manager().connection
        if session.status == .connected { return }
        try session.startVPNTunnel()
        try await wait(session, for: [.connected], timeout: 60)
    }

    func stop() async throws {
        guard let session = try await installed()?.connection else { return }
        if session.status == .disconnected || session.status == .invalid { return }
        session.stopVPNTunnel()
        try await wait(session, for: [.disconnected, .invalid], timeout: 30)
    }

    func status() async throws -> String {
        guard let m = try await installed() else { return "invalid" }
        return Self.name(m.connection.status)
    }

    func watchStatus(f: @escaping @Sendable (String) throws -> Void) -> any Closer {
        let provider = providerID
        let token = NotificationCenter.default.addObserver(forName: .NEVPNStatusDidChange, object: nil, queue: nil) { note in
            guard let session = note.object as? NETunnelProviderSession,
                  (session.manager.protocolConfiguration as? NETunnelProviderProtocol)?.providerBundleIdentifier == provider
            else { return }
            try? f(Self.name(session.status))
        }
        return NativeCloser { NotificationCenter.default.removeObserver(token) }
    }

    /// wait returns once the session reaches one of the states, or throws
    /// what the system said when it fell back to disconnected instead.
    private func wait(_ session: NEVPNConnection, for states: Set<NEVPNStatus>, timeout: TimeInterval) async throws {
        let deadline = Date().addingTimeInterval(timeout)
        var moved = false
        while Date() < deadline {
            try Task.checkCancellation()
            let st = session.status
            if states.contains(st) { return }
            if st == .connecting || st == .disconnecting || st == .reasserting { moved = true }
            if moved && (st == .disconnected || st == .invalid) && !states.contains(st) {
                throw TunnelFailure(await Self.lastError(session) ?? "the VPN stopped while starting")
            }
            try await Task.sleep(nanoseconds: 150_000_000)
        }
        throw TunnelFailure("the VPN did not answer in time")
    }

    private static func lastError(_ session: NEVPNConnection) async -> String? {
        guard #available(iOS 16.0, *) else { return nil }
        do {
            try await session.fetchLastDisconnectError()
            return nil
        } catch {
            return error.localizedDescription
        }
    }

    static func name(_ s: NEVPNStatus) -> String {
        switch s {
        case .invalid: return "invalid"
        case .disconnected: return "disconnected"
        case .connecting: return "connecting"
        case .connected: return "connected"
        case .reasserting: return "reasserting"
        case .disconnecting: return "disconnecting"
        @unknown default: return "disconnected"
        }
    }
}
