#!/bin/bash
# Removes EggBox from this Mac, with its service, and puts the network back
# the way it was.
#
#   sudo packaging/macos/uninstall.sh [--keep-data]
#
# The service is stopped first: it disconnects and removes its routes and DNS
# before it exits, and the program that does so is the one being removed here.
# What the person made in the application (subscriptions, settings) lives in
# their own Library folder and stays; so does the entry of the application in
# System Settings > General > Login Items & Extensions until it is switched off
# there (or removed from inside the application, before this script is run).
set -u

label=org.eggbox.app.agent
app="/Applications/EggBox.app"
data="/Library/Application Support/EggBox"
log="/var/log/$label.log"
package=org.eggbox.app.pkg

keep_data=0
case "${1:-}" in
  "") ;;
  --keep-data) keep_data=1 ;;
  *) echo "usage: $0 [--keep-data]" >&2; exit 2 ;;
esac

if [ "$(id -u)" -ne 0 ]; then
  echo "This changes the system: run it with sudo." >&2
  exit 3
fi

# 1. Stop and unload the service; the system waits for it to finish.
/bin/launchctl bootout "system/$label" >/dev/null 2>&1

# 2. Anything an interrupted run left behind (nothing, normally).
if [ -x "$app/Contents/MacOS/agentd" ]; then
  "$app/Contents/MacOS/agentd" cleanup || echo "the service's own cleanup reported a problem" >&2
fi

# 3. The files.
rm -rf "$app"
[ "$keep_data" -eq 1 ] || rm -rf "$data"
rm -f "$log"
/usr/sbin/pkgutil --forget "$package" >/dev/null 2>&1

echo "EggBox was removed."
echo "If it is still listed in System Settings > General > Login Items & Extensions, switch it off there."
