#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
temporary=$(mktemp -d)
sentinel="$root/.standalone-secret-sentinel"
trap 'rm -rf "$temporary"; rm -f "$sentinel"' EXIT

printf '%s\n' 'must never enter the Docker build context' >"$sentinel"
cat >"$temporary/Dockerfile" <<'EOF'
FROM golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280
WORKDIR /effective-context
COPY . .
RUN test -f go.mod \
 && test -f go.sum \
 && test -d cmd \
 && test -d internal \
 && test ! -e .standalone-secret-sentinel \
 && test ! -e .git \
 && test ! -e docs \
 && test ! -e config
EOF

docker build --no-cache -f "$temporary/Dockerfile" -t fleet-standalone-context-test "$root"
