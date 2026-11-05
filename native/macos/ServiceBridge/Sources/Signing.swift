// What the system says about this program's own code signature.
//
// The agent and the application each guard the channel between them with a
// code signing requirement that names the team that built both. Neither
// carries the team's name in its source: they ask the system whom they
// themselves were signed by, so a build by any other team can talk only to
// itself.

import Foundation
import Security

/// Writes the team identifier this program was signed under into out and
/// returns its length, or -1 when there is none (unsigned or ad hoc signed
/// programs have no team).
@_cdecl("sbui_self_team")
public func sbui_self_team(_ out: UnsafeMutablePointer<CChar>?, _ capacity: Int32) -> Int32 {
    guard let out, capacity > 1 else { return -1 }
    var code: SecCode?
    guard SecCodeCopySelf([], &code) == errSecSuccess, let code else { return -1 }
    var staticCode: SecStaticCode?
    guard SecCodeCopyStaticCode(code, [], &staticCode) == errSecSuccess, let staticCode else { return -1 }
    var info: CFDictionary?
    guard SecCodeCopySigningInformation(staticCode, SecCSFlags(rawValue: kSecCSSigningInformation), &info) == errSecSuccess,
          let dict = info as? [String: Any],
          let team = dict[kSecCodeInfoTeamIdentifier as String] as? String
    else { return -1 }
    let bytes = Array(team.utf8)
    guard bytes.count < Int(capacity) else { return -1 }
    for (i, b) in bytes.enumerated() { out[i] = CChar(bitPattern: b) }
    out[bytes.count] = 0
    return Int32(bytes.count)
}

/// Writes the account name of a user id into out and returns its length, or -1
/// when there is no such user. The system's directory is only reachable
/// through the C library; a Go program without cgo cannot ask it.
@_cdecl("sbui_user_name")
public func sbui_user_name(_ uid: UInt32, _ out: UnsafeMutablePointer<CChar>?, _ capacity: Int32) -> Int32 {
    guard let out, capacity > 1 else { return -1 }
    var scratch = [CChar](repeating: 0, count: 4096)
    // The record points into the scratch buffer, so everything is read inside
    // the closure that holds it still.
    let name: String? = scratch.withUnsafeMutableBufferPointer { buffer in
        var pwd = passwd()
        var result: UnsafeMutablePointer<passwd>?
        guard getpwuid_r(uid_t(uid), &pwd, buffer.baseAddress, buffer.count, &result) == 0,
              result != nil, let raw = pwd.pw_name
        else { return nil }
        return String(cString: raw)
    }
    guard let name else { return -1 }
    let bytes = Array(name.utf8)
    guard !bytes.isEmpty, bytes.count < Int(capacity) else { return -1 }
    for (i, b) in bytes.enumerated() { out[i] = CChar(bitPattern: b) }
    out[bytes.count] = 0
    return Int32(bytes.count)
}
