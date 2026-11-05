#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
temporary=$(mktemp -d)
sentinel="$root/.standalone-secret-sentinel"
trap 'rm -rf "$temporary"; rm -f "$sentinel"' EXIT

printf '%s\n' 'must never enter the Docker build context' >"$sentinel"
cat >"$temporary/Dockerfile" <<'EOF'
FROM golang:1.26.4-alpine@sha256:3ad57304ad93bbec8548a0437ad9e06a455660655d9af011d58b993f6f615648
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
