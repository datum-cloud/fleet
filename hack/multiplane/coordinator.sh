#!/usr/bin/env bash
# Start or stop this run's custody coordinator: the reconciling process that
# admits operations, delivers them through Karmada and accepts the cell's
# results. It runs on the workstation with the coordinator's own token, and
# logs to the private state directory.
set -euo pipefail
action=${1:?start or stop}
state=${2:?private run state directory required}
pidfile="$state/coordinator.pid"
case $action in
 start)
  if [[ -f $pidfile ]] && kill -0 "$(cat "$pidfile")" 2>/dev/null; then
   echo "coordinator: already running"
   exit 0
  fi
  # Extra arguments, such as a shorter installation deadline, pass through.
  nohup "$state/fleet-multiplane-host" coordinator --config "$state/acceptance.json" "${@:3}" >>"$state/coordinator.log" 2>&1 </dev/null &
  echo $! >"$pidfile"
  sleep 2
  kill -0 "$(cat "$pidfile")" || { echo 'coordinator: exited at start; see coordinator.log in the state directory' >&2; exit 1; }
  echo "coordinator: running"
  ;;
 stop)
  if [[ -f $pidfile ]]; then
   pid=$(cat "$pidfile")
   if kill -0 "$pid" 2>/dev/null; then
    kill "$pid"
    for _ in $(seq 1 30); do kill -0 "$pid" 2>/dev/null || break; sleep 1; done
   fi
   rm -f "$pidfile"
  fi
  echo "coordinator: stopped"
  ;;
 restart)
  "$0" stop "$state"
  "$0" start "$state" "${@:3}"
  ;;
 *) echo 'usage: coordinator.sh start|stop|restart STATE [coordinator flags]' >&2; exit 64 ;;
esac
