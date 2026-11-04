#!/bin/sh
set -eu

repository=$(mktemp -d)
trap 'rm -rf "$repository"' EXIT
git -C "$repository" init -q
git -C "$repository" config user.name Fleet-Test
git -C "$repository" config user.email fleet-test@example.invalid
git -C "$repository" config commit.gpgsign false
printf 'one\n' >"$repository/input"
git -C "$repository" add input
git -C "$repository" commit -qm one
first=$(./hack/standalone-source-id.sh "$repository")

git -C "$repository" commit --allow-empty -qm two
second=$(./hack/standalone-source-id.sh "$repository")
if [ "$first" = "$second" ]; then
  echo "distinct clean commits produced the same standalone source ID" >&2
  exit 1
fi

printf 'dirty\n' >>"$repository/input"
dirty=$(./hack/standalone-source-id.sh "$repository")
if [ "$dirty" = "$second" ]; then
  echo "dirty tracked content did not change the standalone source ID" >&2
  exit 1
fi

printf 'untracked\n' >"$repository/untracked"
untracked=$(./hack/standalone-source-id.sh "$repository")
if [ "$untracked" = "$dirty" ]; then
  echo "untracked content did not change the standalone source ID" >&2
  exit 1
fi
