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
  # In CI mode the coordinator and every site process read one Linux kernel
  # clock, so a deadline is declared exceeded after a short margin
  # (FLEET_MULTIPLANE_SKEW_MARGIN, 30 seconds by default). The Colima run keeps
  # production's two minutes: its sites run in a VM whose clock can drift from
  # the workstation's. Extra arguments, such as a shorter installation
  # deadline, pass through.
  if [[ ${FLEET_MULTIPLANE_RUNTIME:-colima} == ci ]]; then skew_default=30s; else skew_default=2m; fi
  skew=${FLEET_MULTIPLANE_SKEW_MARGIN:-$skew_default}
  [[ $skew =~ ^[1-9][0-9]*[sm]$ ]] || { echo 'coordinator: FLEET_MULTIPLANE_SKEW_MARGIN must be seconds or minutes, such as 30s' >&2; exit 64; }
  nohup "$state/fleet-multiplane-host" coordinator --config "$state/acceptance.json" "--skew-margin=$skew" "${@:3}" >>"$state/coordinator.log" 2>&1 </dev/null &
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
