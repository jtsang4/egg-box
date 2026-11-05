# Host snapshots

A snapshot proves a negative ("this run changed nothing outside its sandbox"), so it must be stable when nothing happened and different when something did. That decides what goes in.

## Put in

| Aspect | macOS | Linux | Windows (PowerShell; not exercised by the bundled script) |
| --- | --- | --- | --- |
| Interfaces (names only) | `ifconfig -l` | `/sys/class/net` or `ip -o link` | `Get-NetAdapter \| Sort-Object Name \| Select-Object Name, Status` |
| Default routes | `netstat -rn -f inet` and `-f inet6`, rows whose destination is `default`: destination, gateway, interface | `ip -4 route show default`, `ip -6 route show default` | `Get-NetRoute -DestinationPrefix '0.0.0.0/0','::/0' \| Select-Object InterfaceIndex, NextHop, RouteMetric` |
| System proxy | `scutil --proxy` without the bypass list | `gsettings get org.gnome.system.proxy mode` (desktop dependent) | `netsh winhttp show proxy` and `Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings' ProxyEnable, ProxyServer, AutoConfigURL` |
| Resolvers | `scutil --dns`: the `resolver #` and `nameserver[n]` lines | `/etc/resolv.conf`, `resolvectl dns` | `Get-DnsClientServerAddress \| Sort-Object InterfaceAlias` |
| Your processes | command lines, no pids | same | `Get-CimInstance Win32_Process` filtered by command line |
| Your jobs/services | `launchctl print system/<label>` and `gui/<uid>/<label>`: loaded or absent | `systemctl is-active <unit>` | `Get-Service <name>` |

Also worth adding when relevant: firewall rule names you create, hosts-file hash, files under the state directories you own (names and sizes), keychain items by label.

## Leave out

Anything that changes without cause: process ids, byte counters, timestamps, reachability flags (`scutil --dns` has them), lease times, signal strength, the order of unsorted listings. Sort everything.

## Reading a difference

1. Does it involve your labels, process names, interface names you created, or files under your roots? Then it is yours.
2. Otherwise suspect other software: a proxy or VPN client, Wi-Fi roaming, sleep/wake, Time Machine, MDM. Take another snapshot a minute later and compare the two "before" states if you can: if the host drifts by itself, compare only the aspects you can attribute.
3. Never restore a difference by hand on the user's machine: report it.

## Cheap extra assurance

- Take the baseline and the final snapshot in the same shell session, and keep both files next to the run's logs.
- For a long task, snapshot at the start, before each tier that could matter (packaging, anything that starts processes), and at the end.
- A snapshot that is empty or identical to a different machine's is a broken snapshot: check that the sections are populated before trusting "identical".
