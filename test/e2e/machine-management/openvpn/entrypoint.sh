#!/usr/bin/env bash
set -euo pipefail
openvpn --daemon --dev tun --ifconfig 10.88.0.2 10.88.0.1 \
  --secret /var/run/openvpn/openvpn.key --remote 172.30.0.10 1194 --proto udp \
  --cipher AES-256-CBC --auth SHA256
for _ in $(seq 1 30); do
  ip link show tun0 >/dev/null 2>&1 && break
  sleep 1
done
ip link show tun0 >/dev/null
iptables -t nat -A POSTROUTING -d 10.88.0.1/32 -j MASQUERADE
exec sleep infinity
