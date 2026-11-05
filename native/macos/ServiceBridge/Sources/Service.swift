// Registering the privileged agent as a background service of the application
// (SMAppService). The system, not this code, decides whether the service may
// run: the first registration waits for an administrator to switch it on in
// System Settings, and the bridge only reports where that stands.

import Foundation
import ServiceManagement

/// Where the service stands: 0 not registered, 1 enabled, 2 waiting for
/// approval in System Settings, 3 the application does not contain it.
@_cdecl("sbui_service_status")
public func sbui_service_status(_ plist: UnsafePointer<CChar>) -> Int32 {
    Int32(SMAppService.daemon(plistName: String(cString: plist)).status.rawValue)
}

/// Registers the service. 0 is success; a registration that already exists
/// counts as success. Otherwise the result is the system's error code (a
/// positive number) and the message says what it said.
@_cdecl("sbui_service_register")
public func sbui_service_register(_ plist: UnsafePointer<CChar>, _ err: UnsafeMutablePointer<CChar>?, _ cap: Int32) -> Int32 {
    do {
        try SMAppService.daemon(plistName: String(cString: plist)).register()
        return Code.ok
    } catch let e as NSError {
        if e.code == kSMErrorAlreadyRegistered { return Code.ok }
        ErrorOut(buffer: err, capacity: cap).set(e.localizedDescription)
        return Int32(max(1, e.code))
    }
}

/// Removes the registration.
@_cdecl("sbui_service_unregister")
public func sbui_service_unregister(_ plist: UnsafePointer<CChar>, _ err: UnsafeMutablePointer<CChar>?, _ cap: Int32) -> Int32 {
    do {
        try SMAppService.daemon(plistName: String(cString: plist)).unregister()
        return Code.ok
    } catch let e as NSError {
        ErrorOut(buffer: err, capacity: cap).set(e.localizedDescription)
        return Int32(max(1, e.code))
    }
}

/// Opens the page of System Settings where the person switches the service on.
@_cdecl("sbui_open_login_items")
public func sbui_open_login_items() {
    SMAppService.openSystemSettingsLoginItems()
}
