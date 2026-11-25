#!/usr/bin/env bash
# Boot network DHCP lab. Runs real Kea DHCPv4 and the real synchronizer
# against the site's reservation feed over TLS, on one container bridge, and
# proves with a DHCP client that Kea answers a boot NIC only while its boot
# admission is open. There is no relay and no switch: see
# docs/operations/boot-network.md for what this does not prove.
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
docker create --name "$prefix-feed" --platform linux/amd64 --user 0 --network "$prefix" --network-alias feed --entrypoint sh "$image" \
  -c 'mkdir -p -m 0700 /state/store /state/boot && exec /lab/lab serve -store /state/store -boot /state/boot -token-file /lab/token -cert /lab/feed.crt -key /lab/feed.key' >/dev/null
docker create --name "$prefix-kea" --platform linux/amd64 --user 0 --network "$prefix" --cap-add NET_ADMIN --cap-add NET_RAW --entrypoint sh "$image" \
  -c 'mkdir -p /var/run/kea && (kea-dhcp4 -c /lab/kea.json &) && sleep 2 && exec /lab/fleet-dhcp-sync -feed https://feed:8081 -token-file /lab/token -instance kea-a -kea-socket /var/run/kea/kea4-ctrl-socket -base-config /lab/kea.json -ca-file /lab/feed.crt -interval 1s' >/dev/null
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
client() {
  docker run --rm --platform linux/amd64 --network "$prefix" --mac-address "$1" --cap-add NET_ADMIN --cap-add NET_RAW --entrypoint udhcpc "$image" \
    -i eth0 -n -q -f -t 4 -T 2 -s /bin/true 2>&1 || true
}

await "Kea applies and reports the empty feed" '[ -n "$(latest)" ]'
latest | grep -q '"reservations":\[\]' || fail "the first report was not empty"
client "$mac" | grep -q 'lease of' && fail "Kea answered a boot NIC with no open admission"
echo "PASS no answer before admission"

lab open -operation 00000000-0000-4000-8000-000000000101 -fence 1 -mac "$mac" -address "$address"
await "Kea serves the reservation" 'latest | grep -q "\"$address\""'
lease=$(client "$mac")
echo "$lease" | grep -q "lease of $address obtained" || fail "the reserved NIC did not get $address: $lease"
echo "PASS the reserved boot NIC got $address while its admission was open"
client "$other" | grep -q 'lease of' && fail "Kea answered an unreserved NIC"
echo "PASS an unreserved NIC got no answer"

lab close -operation 00000000-0000-4000-8000-000000000101
await "Kea withdraws the reservation" '! latest | grep -q "\"$address\""'
client "$mac" | grep -q 'lease of' && fail "Kea answered the boot NIC after its admission closed"
echo "PASS no answer after the admission closed"

reports > "$state/evidence.log"
echo "PASS boot network DHCP lab: $(grep -c '' "$state/evidence.log") retained reports from kea-a"
