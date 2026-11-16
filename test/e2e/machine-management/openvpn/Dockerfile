FROM debian:12.12-slim
RUN apt-get update && apt-get install -y --no-install-recommends curl iptables openvpn && rm -rf /var/lib/apt/lists/*
COPY entrypoint.sh /usr/local/bin/entrypoint
RUN chmod 0755 /usr/local/bin/entrypoint
ENTRYPOINT ["/usr/local/bin/entrypoint"]
