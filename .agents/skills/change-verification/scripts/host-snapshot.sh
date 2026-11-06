#!/usr/bin/env bash
# host-snapshot.sh - a deterministic picture of the host's network and service
# state, to prove that a verification run changed nothing.
#
#   host-snapshot.sh [options] > before.txt
#   ... run the verification ...
#   host-snapshot.sh [options] > after.txt && diff before.txt after.txt
#
#   host-snapshot.sh --compare before.txt [options]
#       takes a new snapshot, compares it with before.txt, prints "identical"
#       or a diff, and exits 1 on a difference.
#
# Options (each may be repeated):
#   --process REGEX   list the processes whose command line matches REGEX
#                     (process ids are left out so that runs can be compared)
#   --launchd LABEL   macOS: whether system/LABEL and gui/<uid>/LABEL are loaded
#   --systemd UNIT    Linux: the active state of UNIT
#
# Read-only: nothing here changes the system. Works on macOS and Linux (bash 3.2
# is enough; the route sections of Linux need the `ip` command); for Windows see
# references/host-snapshots.md.
# The output holds local network details (private addresses, interface names):
# keep it out of the repository.
set -u

usage() { awk 'NR > 1 && /^set -u/ { exit } NR > 1 { sub(/^# ?/, ""); print }' "$0"; }

procs=()
launchd=()
units=()
compare=""
while [ $# -gt 0 ]; do
  case "$1" in
    --process | --launchd | --systemd | --compare)
      if [ $# -lt 2 ]; then
        echo "host-snapshot: $1 needs a value" >&2
        exit 2
      fi
      case "$1" in
        --process) procs+=("$2") ;;
        --launchd) launchd+=("$2") ;;
        --systemd) units+=("$2") ;;
        --compare) compare="$2" ;;
      esac
      shift 2
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      echo "host-snapshot: unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

section() { printf '## %s\n' "$1"; }

darwin() {
  section "interfaces"
  ifconfig -l | tr ' ' '\n' | sort
  section "default routes (ipv4)"
  netstat -rn -f inet | awk '$1 == "default" { print $1, $2, $4 }' | sort
  section "default routes (ipv6)"
  netstat -rn -f inet6 | awk '$1 == "default" { print $1, $2, $4 }' | sort
  section "system proxy"
  # The bypass list is long and uninteresting; everything else is the setting.
  scutil --proxy | grep -Ev 'ExceptionsList|^[[:space:]]+[0-9]+ :|^\}' | sort
  section "resolvers"
  scutil --dns | grep -E '^resolver #|nameserver\[' | sort | uniq -c
}

linux() {
  section "interfaces"
  if [ -d /sys/class/net ]; then
    local link
    for link in /sys/class/net/*; do basename "$link"; done | sort
  else
    ip -o link show | awk -F': ' '{ print $2 }' | sort
  fi
  section "default routes (ipv4)"
  ip -4 route show default 2>/dev/null | sort
  section "default routes (ipv6)"
  ip -6 route show default 2>/dev/null | sort
  section "system proxy"
  if command -v gsettings >/dev/null 2>&1; then
    gsettings get org.gnome.system.proxy mode 2>/dev/null || echo "unavailable"
  else
    echo "unavailable"
  fi
  section "resolvers"
  grep -E '^(nameserver|search)' /etc/resolv.conf 2>/dev/null | sort
  if command -v resolvectl >/dev/null 2>&1; then
    resolvectl dns 2>/dev/null | sort
  fi
}

# A process list taken with ps holds this script and the shells around it, and
# their command lines contain the very pattern being looked for. They are left
# out by name, so that two snapshots taken from different command lines agree.
processes() {
  [ ${#procs[@]} -gt 0 ] || return 0
  section "processes"
  local listing re
  listing="$(ps -axo pid=,command= 2>/dev/null | sed -E 's/^[[:space:]]*[0-9]+ //')"
  for re in "${procs[@]}"; do
    printf '%s\n' "$listing" | grep -E -- "$re" | grep -Fv "host-snapshot" | grep -Ev '^(ps|grep|sed) ' | sort
  done
}

launchd_state() {
  [ ${#launchd[@]} -gt 0 ] || return 0
  command -v launchctl >/dev/null 2>&1 || return 0
  section "launchd"
  local label domain
  for label in "${launchd[@]}"; do
    for domain in "system" "gui/$(id -u)"; do
      if launchctl print "$domain/$label" >/dev/null 2>&1; then
        echo "$domain/$label loaded"
      else
        echo "$domain/$label absent"
      fi
    done
  done
}

systemd_state() {
  [ ${#units[@]} -gt 0 ] || return 0
  command -v systemctl >/dev/null 2>&1 || return 0
  section "systemd"
  local unit
  for unit in "${units[@]}"; do
    echo "$unit $(systemctl is-active "$unit" 2>/dev/null || true)"
  done
}

snapshot() {
  case "$(uname -s)" in
    Darwin) darwin ;;
    Linux) linux ;;
    *)
      echo "host-snapshot: $(uname -s) is not supported here; see references/host-snapshots.md" >&2
      exit 2
      ;;
  esac
  processes
  launchd_state
  systemd_state
}

if [ -n "$compare" ]; then
  if [ ! -r "$compare" ]; then
    echo "host-snapshot: cannot read $compare" >&2
    exit 2
  fi
  tmp="$(mktemp "${TMPDIR:-/tmp}/host-snapshot.XXXXXX")"
  trap 'rm -f "$tmp"' EXIT
  snapshot >"$tmp"
  if diff -u "$compare" "$tmp"; then
    echo "identical"
  else
    echo "host-snapshot: the host state differs from $compare" >&2
    exit 1
  fi
  exit 0
fi

snapshot
