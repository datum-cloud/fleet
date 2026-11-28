#!/usr/bin/env bash
# Start or stop this run's central media service: the workstation process,
# beside the coordinator, that serves virtual media to the lab BMC and takes
# first-boot reports. It listens on loopback only; lab Pods reach it
# through the runtime's host gateway, and guests' reports through the
# runtime's report relay, which names each guest's address on the relay
# listener. It reads custody with the
# coordinator's identity, keeps its own state, and logs to the private state
# directory. start extracts the image's pinned artifacts from the site
# image, the same bytes the site serves for network boot.
set -euo pipefail
action=${1:?start or stop}
state=${2:?private run state directory required}
pidfile="$state/media-central.pid"
case $action in
 start)
  if [[ -f $pidfile ]] && kill -0 "$(cat "$pidfile")" 2>/dev/null; then
   echo "media-central: already running"
   exit 0
  fi
  prefix=$(cat "$state/prefix")
  artifacts="$state/media-artifacts"
  if [[ ! -f $artifacts/SHA256SUMS ]]; then
   mkdir -p "$artifacts" && chmod 0700 "$artifacts"
   id=$(docker create "$prefix-site-image" true)
   docker cp "$id:/opt/fleet-local/artifacts/." "$artifacts/" >/dev/null
   docker rm "$id" >/dev/null
  fi
  mkdir -p "$state/media-central" && chmod 0700 "$state/media-central"
  # Clients: lab Pods arrive on loopback, and installed servers' reports
  # through the relay with their own lab network addresses.
  nohup "$state/fleet-multiplane-host" media-service --config "$state/acceptance.json" --listen "127.0.0.1:28443,[::1]:28443" \
   --proxy-listen 127.0.0.1:28444 --relays 127.0.0.0/8 \
   --tls-cert "$state/redfish/media-tls.crt" --tls-key "$state/redfish/media-tls.key" --client-ca "$state/redfish/media-worker-ca.crt" \
   --state-dir "$state/media-central" --artifacts "$artifacts" --clients 127.0.0.0/8,::1/128,192.168.77.0/24 >>"$state/media-central.log" 2>&1 </dev/null &
  echo $! >"$pidfile"
  sleep 2
  kill -0 "$(cat "$pidfile")" || { echo 'media-central: exited at start; see media-central.log in the state directory' >&2; exit 1; }
  echo "media-central: running on loopback; the image's artifacts verified"
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
  echo "media-central: stopped"
  ;;
 *) echo 'usage: media-central.sh start|stop STATE' >&2; exit 64 ;;
esac
