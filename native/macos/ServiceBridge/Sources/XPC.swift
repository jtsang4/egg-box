// The channel between the application and the privileged agent.
//
// The agent speaks gRPC; XPC only introduces the two. A client asks the
// agent's Mach service for a connection, the agent makes a socket pair, keeps
// one end and sends the other back inside the reply (XPC can carry file
// descriptors). Everything after that is an ordinary byte stream that Go
// reads and writes.
//
// The agent verifies who is asking before it hands anything out: the system
// checks the caller's code signature against a requirement the agent sets on
// every incoming connection, and the caller's user id is passed on so the
// agent can compare it with the one user it serves. The caller sets a
// requirement on the agent too, so it does not hand its credentials' worth of
// trust to an impostor.
//
// A listener can also be anonymous, reached by an endpoint rather than a
// name. That needs no registration with the system, which is how this is
// tested without installing anything.

import Foundation
import Security
import XPC

/// Whether the text is a code signing requirement the system can read. XPC
/// does not reject a malformed one: it ends the process, so every requirement
/// is checked before it is handed over.
private func isValidRequirement(_ text: String) -> Bool {
    var requirement: SecRequirement?
    return SecRequirementCreateWithString(text as CFString, SecCSFlags(), &requirement) == errSecSuccess
}

/// Called on the bridge queue for every accepted connection: the agent's end
/// of the socket pair (now owned by the callee), the caller's user id and
/// process id.
public typealias ConnectionCallback = @convention(c) (Int32, UInt32, Int32) -> Void

/// Where results of asynchronous replies are collected without data races.
private final class Outcome: @unchecked Sendable {
    private let lock = NSLock()
    private var fdValue: Int32 = -1
    private var codeValue: Int32 = Code.ok
    private var messageValue = ""
    private var abandoned = false

    func succeed(fd: Int32) {
        lock.lock(); defer { lock.unlock() }
        if abandoned { close(fd); return } // nobody is waiting any more
        fdValue = fd
    }
    func fail(_ code: Int32, _ message: String) {
        lock.lock(); defer { lock.unlock() }
        codeValue = code
        messageValue = message
    }
    func abandon() {
        lock.lock(); defer { lock.unlock() }
        abandoned = true
        if fdValue >= 0 { close(fdValue); fdValue = -1 }
    }
    func read() -> (fd: Int32, code: Int32, message: String) {
        lock.lock(); defer { lock.unlock() }
        return (fdValue, codeValue, messageValue)
    }
}

/// An agent-side listener.
private final class Listener {
    let connection: xpc_connection_t
    let requirement: String?
    let onConnection: ConnectionCallback

    init(connection: xpc_connection_t, requirement: String?, onConnection: @escaping ConnectionCallback) {
        self.connection = connection
        self.requirement = requirement
        self.onConnection = onConnection
    }

    func start() {
        xpc_connection_set_event_handler(connection) { [self] object in
            if xpc_get_type(object) == XPC_TYPE_CONNECTION { accept(object) }
        }
        xpc_connection_activate(connection)
    }

    func accept(_ peer: xpc_connection_t) {
        if let requirement {
            // The system now refuses messages from a caller whose signature
            // does not satisfy this. A requirement it cannot read is a bug in
            // the installation, and nobody gets in.
            if xpc_connection_set_peer_code_signing_requirement(peer, requirement) != 0 {
                xpc_connection_cancel(peer)
                return
            }
        }
        xpc_connection_set_target_queue(peer, bridgeQueue)
        xpc_connection_set_event_handler(peer) { [onConnection] message in
            guard xpc_get_type(message) == XPC_TYPE_DICTIONARY,
                  let reply = xpc_dictionary_create_reply(message) else { return }
            guard let op = xpc_dictionary_get_string(message, "op"), String(cString: op) == "connect" else {
                xpc_dictionary_set_string(reply, "error", "unknown request")
                xpc_connection_send_message(peer, reply)
                return
            }
            var fds: [Int32] = [-1, -1]
            guard socketpair(AF_UNIX, SOCK_STREAM, 0, &fds) == 0 else {
                xpc_dictionary_set_string(reply, "error", "cannot create a socket pair")
                xpc_connection_send_message(peer, reply)
                return
            }
            for fd in fds {
                var one: Int32 = 1
                setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, socklen_t(MemoryLayout<Int32>.size))
                _ = fcntl(fd, F_SETFD, FD_CLOEXEC)
            }
            xpc_dictionary_set_fd(reply, "fd", fds[1]) // the message holds its own copy
            xpc_connection_send_message(peer, reply)
            close(fds[1])
            onConnection(fds[0], xpc_connection_get_euid(peer), xpc_connection_get_pid(peer))
        }
        xpc_connection_activate(peer)
    }

    func stop() { xpc_connection_cancel(connection) }
}

/// Everything the Go side holds by number.
private final class Registry: @unchecked Sendable {
    private let lock = NSLock()
    private var next: Int64 = 1
    private var listeners: [Int64: Listener] = [:]
    private var endpoints: [Int64: xpc_endpoint_t] = [:]

    func add(_ l: Listener) -> Int64 {
        lock.lock(); defer { lock.unlock() }
        let h = next; next += 1
        listeners[h] = l
        return h
    }
    func listener(_ h: Int64) -> Listener? {
        lock.lock(); defer { lock.unlock() }
        return listeners[h]
    }
    func remove(_ h: Int64) -> Listener? {
        lock.lock(); defer { lock.unlock() }
        return listeners.removeValue(forKey: h)
    }
    func addEndpoint(_ e: xpc_endpoint_t) -> Int64 {
        lock.lock(); defer { lock.unlock() }
        let h = next; next += 1
        endpoints[h] = e
        return h
    }
    func endpoint(_ h: Int64) -> xpc_endpoint_t? {
        lock.lock(); defer { lock.unlock() }
        return endpoints[h]
    }
    func removeEndpoint(_ h: Int64) {
        lock.lock(); defer { lock.unlock() }
        endpoints.removeValue(forKey: h)
    }
}

private let registry = Registry()

/// Starts listening. With a service name the listener is the agent's Mach
/// service (the system must have registered that name for this program);
/// without one it is anonymous. The result is a handle, or a negative code.
@_cdecl("sbui_xpc_serve")
public func sbui_xpc_serve(
    _ service: UnsafePointer<CChar>?, _ requirement: UnsafePointer<CChar>?,
    _ onConnection: ConnectionCallback,
    _ err: UnsafeMutablePointer<CChar>?, _ cap: Int32
) -> Int64 {
    // A requirement that cannot be read would let nobody in, or, worse, be
    // skipped by mistake: refuse to start instead.
    if let text = string(requirement), !isValidRequirement(text) {
        ErrorOut(buffer: err, capacity: cap).set("the code signing requirement is not valid")
        return Int64(Code.failed)
    }
    let connection: xpc_connection_t
    if let name = string(service) {
        connection = xpc_connection_create_mach_service(name, bridgeQueue, UInt64(XPC_CONNECTION_MACH_SERVICE_LISTENER))
    } else {
        connection = xpc_connection_create(nil, bridgeQueue)
    }
    let listener = Listener(connection: connection, requirement: string(requirement), onConnection: onConnection)
    let handle = registry.add(listener)
    listener.start()
    return handle
}

/// Makes an endpoint for an anonymous listener, so a client in the same
/// process can reach it. The result is a handle, or a negative code.
@_cdecl("sbui_xpc_endpoint")
public func sbui_xpc_endpoint(_ listener: Int64) -> Int64 {
    guard let l = registry.listener(listener) else { return Int64(Code.failed) }
    return registry.addEndpoint(xpc_endpoint_create(l.connection))
}

/// Stops a listener.
@_cdecl("sbui_xpc_stop")
public func sbui_xpc_stop(_ handle: Int64) {
    registry.remove(handle)?.stop()
}

/// Forgets an endpoint.
@_cdecl("sbui_xpc_release_endpoint")
public func sbui_xpc_release_endpoint(_ handle: Int64) {
    registry.removeEndpoint(handle)
}

/// Asks the agent's Mach service for a connection. The result is a file
/// descriptor (zero or more), or a negative code: -2 when the service is not
/// there (not registered, not allowed yet, or not running and unable to
/// start), -3 on timeout.
@_cdecl("sbui_xpc_connect")
public func sbui_xpc_connect(
    _ service: UnsafePointer<CChar>, _ requirement: UnsafePointer<CChar>?, _ timeoutMs: Int32,
    _ err: UnsafeMutablePointer<CChar>?, _ cap: Int32
) -> Int32 {
    // The system domain: the agent is a daemon, not an agent of the user's session.
    let name = String(cString: service)
    return exchange({ xpc_connection_create_mach_service(name, bridgeQueue, UInt64(XPC_CONNECTION_MACH_SERVICE_PRIVILEGED)) },
                    requirement: string(requirement), timeoutMs: timeoutMs, ErrorOut(buffer: err, capacity: cap))
}

/// Asks an anonymous listener, by its endpoint, for a connection.
@_cdecl("sbui_xpc_connect_endpoint")
public func sbui_xpc_connect_endpoint(
    _ endpoint: Int64, _ requirement: UnsafePointer<CChar>?, _ timeoutMs: Int32,
    _ err: UnsafeMutablePointer<CChar>?, _ cap: Int32
) -> Int32 {
    guard let e = registry.endpoint(endpoint) else {
        ErrorOut(buffer: err, capacity: cap).set("no such endpoint")
        return Code.failed
    }
    return exchange({ xpc_connection_create_from_endpoint(e) },
                    requirement: string(requirement), timeoutMs: timeoutMs, ErrorOut(buffer: err, capacity: cap))
}

/// The one request the protocol has, from the caller's side.
private func exchange(_ makeConnection: () -> xpc_connection_t, requirement: String?, timeoutMs: Int32, _ err: ErrorOut) -> Int32 {
    // Before any connection exists: libxpc ends the process over a malformed
    // requirement, and over cancelling a connection that was never started.
    if let requirement, !isValidRequirement(requirement) {
        err.set("the code signing requirement of the service is not valid")
        return Code.failed
    }
    let connection = makeConnection()
    if let requirement {
        guard xpc_connection_set_peer_code_signing_requirement(connection, requirement) == 0 else {
            err.set("the code signing requirement of the service was refused by the system")
            return Code.failed
        }
    }
    xpc_connection_set_event_handler(connection) { _ in }
    xpc_connection_activate(connection)

    let request = xpc_dictionary_create(nil, nil, 0)
    xpc_dictionary_set_string(request, "op", "connect")
    let outcome = Outcome()
    let done = DispatchSemaphore(value: 0)
    xpc_connection_send_message_with_reply(connection, request, bridgeQueue) { reply in
        defer { done.signal() }
        if xpc_get_type(reply) == XPC_TYPE_ERROR {
            var why = "the service is not available"
            if let d = xpc_dictionary_get_string(reply, XPC_ERROR_KEY_DESCRIPTION) { why = String(cString: d) }
            outcome.fail(Code.unavailable, why)
            return
        }
        if let e = xpc_dictionary_get_string(reply, "error") {
            outcome.fail(Code.failed, String(cString: e))
            return
        }
        let fd = xpc_dictionary_dup_fd(reply, "fd")
        if fd < 0 {
            outcome.fail(Code.failed, "the service sent no connection")
            return
        }
        outcome.succeed(fd: fd)
    }

    if done.wait(timeout: .now() + .milliseconds(Int(timeoutMs))) == .timedOut {
        outcome.abandon()
        xpc_connection_cancel(connection)
        err.set("the service did not answer in time")
        return Code.timeout
    }
    xpc_connection_cancel(connection) // the descriptor does not depend on it
    let r = outcome.read()
    if r.fd < 0 {
        err.set(r.message)
        return r.code == Code.ok ? Code.failed : r.code
    }
    return r.fd
}
