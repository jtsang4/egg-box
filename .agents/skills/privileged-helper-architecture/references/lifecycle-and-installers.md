# Install, upgrade, uninstall

The installer runs privileged on somebody else's computer. Keep its scripts short, idempotent, and never able to fail an installation over a courtesy step; cover them with `shellcheck` and with tests that tie their spelling to the code.

## Three independent switches

| Switch | Level | Where it lives |
| --- | --- | --- |
| The service starts with the computer | system (needs administrator, or on macOS the person's approval in System Settings) | the OS service registration |
| The app starts at login | the user | a per-user login item / startup entry |
| Connect automatically when the app starts | the app | a service setting (intent) |

Turning one off never silently changes another. In the UI each is its own row with its own status; the row for a switch the platform cannot toggle in-app (macOS boot start) opens System Settings instead.

## Install

- Put the files in a location only administrators can change (macOS `/Applications`, Windows `Program Files`); the package is non-relocatable and root-owned. The service refuses to run from anywhere else.
- Register the service but connect nothing. The first run of the app performs the authorization: on macOS it shows the states "not registered", "enabled", "waiting for approval in System Settings" and "service missing from this build", and guides the person; on Windows it asks for administrator rights once to install and start the service.
- A re-install replaces the older registration but keeps what the person chose about starting with the computer.

## First connection of a new owner

Described in `authorization-and-ipc.md`: the console user claims the unowned service; the installer may claim it for the person at the screen only when nobody owns it.

## Upgrade

The aim: replace the whole package and bring back the connection the person had, without ever leaving the network half-configured.

1. Disconnect and clean up before the files change. Either the app asks for it (`PrepareUpgrade`: disconnect, clean up, record the intent), or the installer's pre-install step asks the OS service manager to stop the service with a termination signal, which the service answers by disconnecting and restoring the network before it exits. Signal the job through the service manager, not by looking for a process name (it runs as the same short name whatever folder it came from).
2. If the service was running, leave a note in its data directory saying the stop is for an upgrade (`upgrade-pending`). Only an installer can write there (root or administrators), so only an installer can leave it.
3. Replace the **whole** package, never a mix of versions; verify signature and version.
4. Start the service. At start it consumes the marker: the marker is always deleted, only a plain file counts (a link or directory in its place is removed and ignored), and the previous intent is resumed only if the person was connected.
5. A stop **without** the marker (computer shutting down, the person stopping the service) restores nothing by itself; reconnecting is then up to the settings.
6. Nothing in the installer's script may fail the installation: with no service running there is nothing to do.

Installer scripts spell the marker's name (and the service name, label and paths) because they cannot import the Go package; a guard test reads both and fails if they differ.

## Version skew

The handshake carries the protocol version and the service's own version. An older app against a newer service, or the reverse, gets a specific message and a way out (update, or restart the service), not an obscure RPC error.

## Uninstall

- Stop the service so it runs its normal disconnect-and-clean path; then run the privileged cleanup command that releases anything still in the ledger; remove the service registration; remove the files.
- Ask whether to keep user data (profiles, settings); never leave a TUN, resolver override or route behind.
- The uninstaller runs as root: verify it statically (shellcheck, read it, test its pieces against fakes) and never run it with `sudo` on a development machine.

## Logging and diagnosing a service nobody watches

- Log to a file or ring buffer an administrator can read, with levels and a bounded size; expose a redacted diagnostics report through the API.
- Write start-up failures to a file as well: a service has no console, launchd discards a job's output unless the plist names `StandardOutPath` and `StandardErrorPath` (point both at a log file), and a Windows service has no valid stderr handle.
- The report says which driver is active, the ledger phase and entries, the last cleanup outcome (a failed sweep must be visible), and versions; it redacts addresses and names (`secret-safe-testing`).
