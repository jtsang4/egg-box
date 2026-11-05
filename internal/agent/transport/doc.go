// Package transport holds the local channels between the GUI and the agent.
//
// A channel is only a way to reach the agent and a way for the agent to learn
// who is calling; what is allowed once connected is decided by the API.
//
//   - Windows: a named pipe whose ACL admits SYSTEM, administrators and the
//     authorized user, and whose clients are verified by process and user.
//   - macOS: connections handed over by the XPC bridge, which verified the
//     peer's code-signing requirement and user.
//   - Development builds (build tag eggbox_dev): a Unix socket in a directory
//     the developer owns, verified by the peer's user id. This transport is not
//     compiled into release binaries, so a release agent cannot be made to
//     listen on a path an attacker can pick.
package transport
