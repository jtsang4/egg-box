# Authorization and IPC

Two questions, answered in two places:

1. **Who may connect?** The transport and a peer verifier. The transport is only a way to reach the service and a way for it to learn who is calling.
2. **What may a connected peer do?** The API, the controller and the policy gate. Nothing here depends on which transport carried the call.

Keeping them apart lets the same gRPC stack run over a Windows named pipe, a macOS XPC-introduced socket and a developer's unix socket, and lets a test client exercise the same behaviour without any of them.

## The owner model

One user may control the service. Nobody types who it is during installation: an installer runs as an administrator (possibly a different account from the person), and a typed name can be wrong or spoofed.

- The first user who asks, and who is the person at the screen (the console user), becomes the owner. The record is stored in the protected data directory (SID on Windows, uid on macOS; a display name for the UI only).
- From then on only that user is admitted. Resetting is an administrator's action (a command, or removing the record); the next user can claim the service.
- On macOS root is never the application's user. A background process of another account cannot claim the service first, because only the console user may claim.
- An installer may claim ownership for the person at the screen only when there is no owner yet (`authorize --if-unowned`).

## Fail closed

Every missing piece refuses, with an error that says why: no owner, not a well-formed SID, "a signature is required but nothing can check it", a client image outside the install directory, an invalid requirement string. A policy that can be configured into "allow" by a missing value is a bug.

## Windows: named pipe

- Create the pipe with an explicit security descriptor, never the default (the default lets Everyone read). In SDDL: deny network logons; allow SYSTEM and Administrators everything; allow the owner's SID read and write; nobody else can even open it. Deny entries come first, as in a canonical ACL, so reordering cannot change the meaning.
- Validate the SID string with a strict pattern before building the descriptor string.
- After connect, verify the peer's facts: user SID equals the owner; the executable's image path is under the install directory (compare as Windows does: either slash, case-insensitive, `.` and `..` resolved, a directory is not inside itself); a valid Authenticode signature when the service itself is signed.
- Keep that decision in a pure `PeerPolicy.Check(PeerFacts)` so it is tested on any OS; the Windows-only file gathers the facts (token SID, process image) and asks it.

## macOS: XPC introduces, a socket carries

- A LaunchDaemon exposes a Mach service. XPC only introduces the two parties: the client asks for a connection, the service creates a socket pair, keeps one end and sends the other back in the reply (XPC carries file descriptors). From then on both sides run ordinary gRPC over a byte stream, identical to the Windows path (`net.Conn` in, `Verifier(conn) -> Peer` out).
- The system verifies the caller's code signature against a requirement the service sets on every incoming connection. The client sets a requirement on the service too, so it does not hand its trust to an impostor.
- Requirements name the team that signed both programs. Neither carries the team name in source: each asks the system who signed it. An ad hoc or unsigned build has no team, so it can only talk to itself (right for development and CI).
- **Validate every requirement string with `SecRequirementCreateWithString` before use.** XPC does not reject a malformed requirement: it terminates the process.
- The caller's uid arrives with the connection; the uid policy compares it with the owner (rules above).
- An **anonymous listener** reached by an endpoint rather than a name needs no registration with the system. That is how the handshake, the fd hand-over and the requirement checks are tested without installing anything.

## Development transport

A unix socket inside a directory the developer owns (0700), peer verified by uid (`SO_PEERCRED` on Linux, `LOCAL_PEERCRED` on macOS), compiled only under the dev build tag so a release service cannot be made to listen on a path an attacker can choose. It refuses to run as root.

## gRPC glue

- Wrap the verifier in custom `TransportCredentials`: the handshake step runs `Verifier(conn)`, attaches the resulting peer to the call context, and the server logs "who did what" from it.
- The peer description appears in the Handshake response so tests (and the UI's diagnostics) can see who the service thinks is calling.
- Authorize per **connection** (the peer was verified when the connection was made), not per method name. Interceptors that match on method names can be bypassed with a malformed request path: an advisory of exactly that kind (an authorization bypass through a path without its leading slash) was among the reachable vulnerabilities a reachability-aware scan found late in the project behind this skill, and upgrading the dependency fixed it.
