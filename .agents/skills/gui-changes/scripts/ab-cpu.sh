#!/usr/bin/env bash
# ab-cpu.sh - compare the CPU use of two or more commands fairly, for judging a
# performance change in a program that is meant to sit idle (a window with
# animation, a tray app, a daemon).
#
#   ab-cpu.sh [-r ROUNDS] [-w WARMUP] [-d WINDOW] -- 'command A' 'command B' ...
#
#   -r  rounds                      (default 4)
#   -w  seconds to settle first     (default 6)
#   -d  seconds measured per round  (default 12; on Linux ps reports whole
#       seconds, so use 30 or more there)
#
# Every round starts each command afresh (as `bash -c "exec <command>"`, with
# output discarded), waits for the warm-up, and takes the CPU time the process
# used over the window as a percentage of one core. The order alternates
# between rounds (A B, then B A, ...) so that a slowly drifting machine does
# not favour either side. Prints each round's figures and the median.
#
# One measurement of such a program is easily out by a factor of two, and a
# change that does not beat the spread between the rounds of the same command
# has no measured effect: revert it.
set -u

usage() { awk 'NR > 1 && /^set -u/ { exit } NR > 1 { sub(/^# ?/, ""); print }' "$0"; }

rounds=4
warmup=6
window=12
while [ $# -gt 0 ]; do
  case "$1" in
    -r | -w | -d)
      if [ $# -lt 2 ]; then
        echo "ab-cpu: $1 needs a value" >&2
        exit 2
      fi
      case "$2" in '' | *[!0-9]*)
        echo "ab-cpu: $1 needs a whole number of seconds or rounds" >&2
        exit 2
        ;;
      esac
      case "$1" in
        -r) rounds="$2" ;;
        -w) warmup="$2" ;;
        -d) window="$2" ;;
      esac
      shift 2
      ;;
    --)
      shift
      break
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      echo "ab-cpu: unknown argument: $1 (the commands come after --)" >&2
      exit 2
      ;;
  esac
done
if [ $# -lt 2 ]; then
  echo "ab-cpu: give at least two commands after --" >&2
  exit 2
fi
if [ "$window" -lt 1 ] || [ "$rounds" -lt 1 ]; then
  echo "ab-cpu: the window and the number of rounds must be at least 1" >&2
  exit 2
fi

commands=("$@")
count=${#commands[@]}

# CPU seconds a process has used so far. ps prints [[dd-]hh:]mm:ss[.cc].
cpu_seconds() {
  ps -o time= -p "$1" 2>/dev/null | awk '
    NF == 0 { exit 1 }
    {
      t = $1; days = 0
      if (index(t, "-")) { split(t, d, "-"); days = d[1]; t = d[2] }
      n = split(t, a, ":"); s = 0
      for (i = 1; i <= n; i++) s = s * 60 + a[i]
      print days * 86400 + s
    }'
}

current=""
stop_current() {
  if [ -n "$current" ]; then
    kill -TERM "$current" 2>/dev/null
    sleep 1
    kill -KILL "$current" 2>/dev/null
    wait "$current" 2>/dev/null
    current=""
  fi
}
trap 'stop_current' EXIT
trap 'exit 130' INT TERM

# measure INDEX: sets $measured to the percentage of one core, or "n/a" if the
# program was not alive for the whole window. (It sets a variable instead of
# printing so that the process stays a child of this shell, where the traps can
# stop it when the script is interrupted.)
measured=""
measure() {
  bash -c "exec ${commands[$1]}" </dev/null >/dev/null 2>&1 &
  current=$!
  sleep "$warmup"
  local a b
  a="$(cpu_seconds "$current")" || a=""
  sleep "$window"
  b="$(cpu_seconds "$current")" || b=""
  if [ -n "$a" ] && [ -n "$b" ] && kill -0 "$current" 2>/dev/null; then
    measured="$(awk -v a="$a" -v b="$b" -v w="$window" 'BEGIN { printf "%.1f", (b - a) / w * 100 }')"
  else
    measured="n/a"
  fi
  stop_current
}

results=()
i=0
while [ "$i" -lt "$count" ]; do
  results+=("")
  i=$((i + 1))
done

round=1
while [ "$round" -le "$rounds" ]; do
  order=()
  i=0
  while [ "$i" -lt "$count" ]; do
    order+=("$i")
    i=$((i + 1))
  done
  if [ $((round % 2)) -eq 0 ]; then
    reversed=()
    for ((i = count - 1; i >= 0; i--)); do reversed+=("${order[$i]}"); done
    order=("${reversed[@]}")
  fi
  for i in "${order[@]}"; do
    measure "$i"
    results[i]="${results[i]} $measured"
  done
  round=$((round + 1))
done

label() { # first word of the command, without its directory
  local w="${1%% *}"
  basename "$w"
}

letters="ABCDEFGHIJKLMNOPQRSTUVWXYZ"
i=0
while [ "$i" -lt "$count" ]; do
  median="$(printf '%s\n' "${results[$i]}" | tr ' ' '\n' | grep -v -e '^$' -e 'n/a' | sort -n |
    awk '{ v[NR] = $1 } END {
      if (NR == 0) print "n/a"
      else if (NR % 2) printf "%.1f", v[(NR + 1) / 2]
      else printf "%.1f", (v[NR / 2] + v[NR / 2 + 1]) / 2
    }')"
  printf '%s  %-24s %s   median %s %% of one core\n' "${letters:$i:1}" "$(label "${commands[$i]}")" "${results[$i]}" "$median"
  i=$((i + 1))
done
