#!/usr/bin/env bash
# Boot network DHCP lab. Runs real Kea DHCPv4 and the real synchronizer
# against the site's reservation feed over TLS, on one container bridge, and
# proves with a DHCP client that Kea answers a boot NIC only while its boot
# admission is open. Kea runs with the site component's capabilities and
# control socket directory. There is no relay and no switch, so Kea uses a raw
# socket here: see docs/operations/boot-network.md for what this does not
# prove.
#
# Exit codes: 0 passed, 1 failed.
set -euo pipefail
cd "$(dirname "$0")/../.."
export DOCKER_CONTEXT="${DOCKER_CONTEXT:-colima}"
image=docker.cloudsmith.io/isc/docker/kea-dhcp4:2.6.3@sha256:c40abb1522b15edb9a6d1119ad22b3765bf818a51d6776f498e874ad13416065
prefix="fleet-kea-lab-$(openssl rand -hex 4)"
state=$(mktemp -d)
mac=02:00:5e:00:53:01
other=02:00:5e:00:53:02
address=198.51.100.200

cleanup() {
  docker rm -f "$prefix-feed" "$prefix-kea" >/dev/null 2>&1 || true
  docker network rm "$prefix" >/dev/null 2>&1 || true
  rm -rf "$state"
}
trap cleanup EXIT
fail() { echo "FAIL $*" >&2; docker logs "$prefix-kea" 2>&1 | tail -20 >&2 || true; exit 1; }

echo "building linux/amd64 binaries"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$state/fleet-dhcp-sync" ./cmd/fleet-dhcp-sync
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$state/lab" ./hack/boot-network/lab
openssl rand -hex 32 > "$state/token"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 -subj /CN=feed \
  -addext subjectAltName=DNS:feed -keyout "$state/feed.key" -out "$state/feed.crt" 2>/dev/null
# The network owner's base configuration: one provisioning subnet, its
# gateway, no pool, so only reserved MACs get an answer.
cat > "$state/kea.json" <<'EOF'
{"Dhcp4": {
  "interfaces-config": {"interfaces": ["eth0"], "dhcp-socket-type": "raw"},
  "control-socket": {"socket-type": "unix", "socket-name": "/var/run/kea/kea4-ctrl-socket"},
  "lease-database": {"type": "memfile", "persist": false},
  "host-reservation-identifiers": ["hw-address"],
  "valid-lifetime": 120,
  "subnet4": [{"id": 1, "subnet": "198.51.100.0/24", "option-data": [{"name": "routers", "data": "198.51.100.1"}]}]
}}
EOF

# Docker assigns container addresses from the lower half only, so it never
# hands out the reserved address.
docker network create --subnet 198.51.100.0/24 --ip-range 198.51.100.0/25 --gateway 198.51.100.1 "$prefix" >/dev/null
# The feed reads the site's synced enrollment, as the site's dhcp-feed role
# does: it serves, and takes reports from, only the expected instance kea-a.
enrollment='{"dhcp":"synced","dhcpInstances":["kea-a"],"targets":[{"device":"fleet-provider/server-example-001","endpoint":"https://192.0.2.10:8443","systemID":"System-1","systemUUID":"00000000-0000-4000-8000-000000000001","diskBytes":2147483648,"console":"lab.log"}]}'
docker create --name "$prefix-feed" --platform linux/amd64 --user 0 --network "$prefix" --network-alias feed -e FLEET_REDFISH_ENROLLMENT="$enrollment" --entrypoint sh "$image" \
  -c 'mkdir -p -m 0700 /state/store /state/boot && exec /lab/lab serve -store /state/store -boot /state/boot -token-file /lab/token -cert /lab/feed.crt -key /lab/feed.key' >/dev/null
# As in the site component: the socket directory starts 0777, like an
# emptyDir, and Kea's command makes it 0750 before Kea starts.
docker create --name "$prefix-kea" --platform linux/amd64 --user 0 --network "$prefix" --cap-drop ALL --cap-add NET_BIND_SERVICE --cap-add NET_RAW --tmpfs /var/run/kea:mode=0777 --entrypoint sh "$image" \
  -c '(sh -c "chmod 0750 /var/run/kea && exec kea-dhcp4 -c /lab/kea.json" &) && sleep 2 && exec /lab/fleet-dhcp-sync -feed https://feed:8081 -token-file /lab/token -instance kea-a -kea-socket /var/run/kea/kea4-ctrl-socket -base-config /lab/kea.json -ca-file /lab/feed.crt -interval 1s' >/dev/null
# The containers run as root without CAP_DAC_OVERRIDE, as the component's do,
# so the lab files must be readable by any user. They are synthetic and
# deleted on exit.
chmod 0755 "$state"
chmod 0644 "$state"/token "$state"/feed.key "$state"/feed.crt "$state"/kea.json
for container in feed kea; do
  docker cp "$state/." "$prefix-$container:/lab" >/dev/null
  docker start "$prefix-$container" >/dev/null
done

lab() { docker exec "$prefix-feed" /lab/lab "$1" -store /state/store -boot /state/boot "${@:2}"; }
reports() { docker exec "$prefix-feed" cat /state/boot/dhcp-applied.log 2>/dev/null || true; }
# latest prints kea-a's most recent applied feed.
latest() { reports | grep '"instance":"kea-a"' | tail -1; }
await() {
  local what=$1 check=$2
  for _ in $(seq 1 90); do
    if eval "$check"; then return 0; fi
    sleep 1
  done
  fail "timed out: $what"
}
# client asks for an address with one MAC. udhcpc prints "lease of" on an
# answer and "no lease" after its retries; anything else is a lab failure.
client() {
  docker run --rm --platform linux/amd64 --network "$prefix" --mac-address "$1" --cap-add NET_ADMIN --cap-add NET_RAW --entrypoint udhcpc "$image" \
    -i eth0 -n -q -f -t 4 -T 2 -s /bin/true 2>&1 || true
}
refused() {
  local out
  out=$(client "$1")
  echo "$out" | grep -q 'lease of' && fail "$2"
  echo "$out" | grep -q 'no lease' || fail "the DHCP client did not run: $out"
}

await "Kea applies and reports the empty feed" '[ -n "$(latest)" ]'
latest | grep -q '"reservations":\[\]' || fail "the first report was not empty"
refused "$mac" "Kea answered a boot NIC with no open admission"
echo "PASS no answer before admission"

lab open -operation 00000000-0000-4000-8000-000000000101 -fence 1 -mac "$mac" -address "$address"
await "Kea serves the reservation" 'latest | grep -q "\"$address\""'
lease=$(client "$mac")
echo "$lease" | grep -q "lease of $address obtained" || fail "the reserved NIC did not get $address: $lease"
echo "PASS the reserved boot NIC got $address while its admission was open"
refused "$other" "Kea answered an unreserved NIC"
echo "PASS an unreserved NIC got no answer"

lab close -operation 00000000-0000-4000-8000-000000000101
await "Kea withdraws the reservation" '! latest | grep -q "\"$address\""'
refused "$mac" "Kea answered the boot NIC after its admission closed"
echo "PASS no answer after the admission closed"

echo "Applied reports the site retained:"
reports
echo "PASS boot network DHCP lab"
