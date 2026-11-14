#!/bin/sh
set -eu

repository=${1:-.}
manifest=$(mktemp)
trap 'rm -f "$manifest"' EXIT

git -C "$repository" rev-parse HEAD >"$manifest"
git -C "$repository" rev-parse 'HEAD^{tree}' >>"$manifest"
git -C "$repository" diff --binary HEAD >>"$manifest"
git -C "$repository" ls-files --others --exclude-standard | sort | while IFS= read -r file; do
  digest=$(shasum -a 256 "$repository/$file" | cut -d ' ' -f 1)
  printf '%s  %s\n' "$digest" "$file"
done >>"$manifest"

shasum -a 256 "$manifest" | cut -c1-16
