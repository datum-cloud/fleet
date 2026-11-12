#!/usr/bin/env bash
set -euo pipefail
kustomize build config/overlays/synthetic-staging | go run ./hack/synthetic-staging-validator.go
