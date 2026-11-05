// Who is at the screen, and whether the computer is going to sleep.
//
// The agent runs whether or not anybody is signed in. It serves one user, and
// it must hear when that user leaves (the connection ends then) and when the
// computer sleeps or wakes (the uplink is probably not the same afterwards).

import Foundation
import IOKit
import IOKit.pwr_mgt
import SystemConfiguration

/// A change of the person at the screen: their user id, or -1 for nobody.
public typealias ConsoleCallback = @convention(c) (Int32) -> Void
/// A change of power state: 0 going to sleep, 1 awake again.
public typealias PowerCallback = @convention(c) (Int32) -> Void

/// The user id of the person at the screen, or -1 when the login window is
/// showing.
@_cdecl("sbui_console_user")
public func sbui_console_user() -> Int64 {
    var uid: uid_t = 0
    var gid: gid_t = 0
    guard let name = SCDynamicStoreCopyConsoleUser(nil, &uid, &gid) as String?, name != "loginwindow" else { return -1 }
    return Int64(uid)
}

private final class ConsoleWatcher {
    let callback: ConsoleCallback
    var store: SCDynamicStore?
    init(_ callback: @escaping ConsoleCallback) { self.callback = callback }
}

private final class PowerWatcher {
    let callback: PowerCallback
    var root: io_connect_t = 0
    var port: IONotificationPortRef?
    var notifier: io_object_t = 0
    init(_ callback: @escaping PowerCallback) { self.callback = callback }
}

private final class Watchers: @unchecked Sendable {
    private let lock = NSLock()
    private var next: Int64 = 1
    private var console: [Int64: ConsoleWatcher] = [:]
    private var power: [Int64: PowerWatcher] = [:]

    func add(_ w: ConsoleWatcher) -> Int64 { lock.lock(); defer { lock.unlock() }; let h = next; next += 1; console[h] = w; return h }
    func add(_ w: PowerWatcher) -> Int64 { lock.lock(); defer { lock.unlock() }; let h = next; next += 1; power[h] = w; return h }
    func take(_ h: Int64) -> (ConsoleWatcher?, PowerWatcher?) {
        lock.lock(); defer { lock.unlock() }
        return (console.removeValue(forKey: h), power.removeValue(forKey: h))
    }
}

private let watchers = Watchers()

/// Starts calling back when the person at the screen changes. The result is a
/// handle, or a negative code.
@_cdecl("sbui_watch_console")
public func sbui_watch_console(_ callback: ConsoleCallback) -> Int64 {
    let watcher = ConsoleWatcher(callback)
    var context = SCDynamicStoreContext(
        version: 0, info: Unmanaged.passUnretained(watcher).toOpaque(), retain: nil, release: nil, copyDescription: nil)
    let changed: SCDynamicStoreCallBack = { _, _, info in
        guard let info else { return }
        let w = Unmanaged<ConsoleWatcher>.fromOpaque(info).takeUnretainedValue()
        w.callback(Int32(truncatingIfNeeded: sbui_console_user()))
    }
    guard let store = SCDynamicStoreCreate(nil, "me.jtsang.singboxui.watch" as CFString, changed, &context),
          SCDynamicStoreSetNotificationKeys(store, ["State:/Users/ConsoleUser"] as CFArray, nil),
          SCDynamicStoreSetDispatchQueue(store, bridgeQueue) else { return Int64(Code.failed) }
    watcher.store = store
    return watchers.add(watcher)
}

// What the power system sends (IOKit's messages are macros Swift cannot see).
private let canSystemSleep: UInt32 = 0xe000_0270
private let systemWillSleep: UInt32 = 0xe000_0280
private let systemHasPoweredOn: UInt32 = 0xe000_0300

/// Starts calling back on sleep and wake. The result is a handle, or a
/// negative code.
@_cdecl("sbui_watch_power")
public func sbui_watch_power(_ callback: PowerCallback) -> Int64 {
    let watcher = PowerWatcher(callback)
    let interest: IOServiceInterestCallback = { refcon, _, message, argument in
        guard let refcon else { return }
        let w = Unmanaged<PowerWatcher>.fromOpaque(refcon).takeUnretainedValue()
        switch message {
        case canSystemSleep:
            // Never object: the agent has no reason to keep the computer awake.
            IOAllowPowerChange(w.root, Int(bitPattern: argument))
        case systemWillSleep:
            w.callback(0)
            IOAllowPowerChange(w.root, Int(bitPattern: argument))
        case systemHasPoweredOn:
            w.callback(1)
        default:
            break
        }
    }
    watcher.root = IORegisterForSystemPower(Unmanaged.passUnretained(watcher).toOpaque(), &watcher.port, interest, &watcher.notifier)
    guard watcher.root != 0, let port = watcher.port else { return Int64(Code.failed) }
    IONotificationPortSetDispatchQueue(port, bridgeQueue)
    return watchers.add(watcher)
}

/// Stops a watcher of either kind.
@_cdecl("sbui_watch_stop")
public func sbui_watch_stop(_ handle: Int64) {
    let (console, power) = watchers.take(handle)
    if let console, let store = console.store {
        SCDynamicStoreSetDispatchQueue(store, nil)
        console.store = nil
    }
    if let power {
        IODeregisterForSystemPower(&power.notifier)
        IOServiceClose(power.root)
        if let port = power.port { IONotificationPortDestroy(port) }
        power.port = nil
    }
}
