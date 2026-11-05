// Support shared by the bridge's entry points.
//
// Every function the Go side calls is a plain C function (@_cdecl) that takes
// its text as C strings and reports a failure by a negative result plus a
// message written into a buffer the caller provides. That keeps the calls
// thread-safe without any hidden "last error" state, and means no Swift or
// Objective-C object ever crosses into Go.

import Foundation

/// The error buffer of a call: where to write the message, and how much fits.
struct ErrorOut {
    let buffer: UnsafeMutablePointer<CChar>?
    let capacity: Int32

    func set(_ message: String) {
        guard let buffer, capacity > 0 else { return }
        let bytes = Array(message.utf8.prefix(Int(capacity) - 1))
        for (i, b) in bytes.enumerated() { buffer[i] = CChar(bitPattern: b) }
        buffer[bytes.count] = 0
    }
}

/// Reads an optional C string.
func string(_ p: UnsafePointer<CChar>?) -> String? {
    guard let p else { return nil }
    return String(cString: p)
}

/// The queue everything asynchronous runs on, so callbacks into Go never run
/// concurrently with each other.
let bridgeQueue = DispatchQueue(label: "org.eggbox.app.bridge")

/// The result codes of the calls that can fail.
enum Code {
    static let ok: Int32 = 0
    static let failed: Int32 = -1
    /// The other end is not there: no such Mach service, or it is not allowed to run yet.
    static let unavailable: Int32 = -2
    static let timeout: Int32 = -3
    static let denied: Int32 = -4
}
