#!/usr/bin/env bash
# Offline tests for validate-doc-links.sh. The fixtures are written to a
# scratch directory, so the repository's own check never sees them.
set -euo pipefail
root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
check="$root/hack/validate-doc-links.sh"
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
failures=0

expect() {
  local name=$1 want=$2 got=0
  (cd "$scratch/$name" && bash "$check" >"$scratch/$name.out" 2>&1) || got=$?
  if [[ $want == pass && $got -ne 0 || $want == fail && $got -eq 0 ]]; then
    echo "FAIL $name: expected $want, exit $got: $(cat "$scratch/$name.out")" >&2
    failures=1
  fi
}

fixture() { mkdir -p "$scratch/$1/docs"; cat >"$scratch/$1/docs/page.md"; touch "$scratch/$1/docs/present.md"; }

fixture good <<'MD'
A [link on one line](present.md) and a [link whose text
wraps across a line break](present.md#section), and [external](https://example.com).
MD
expect good pass

fixture broken-one-line <<'MD'
A [broken link](missing.md).
MD
expect broken-one-line fail

fixture broken-wrapped <<'MD'
A [broken link whose text
wraps across a line break](missing-wrapped.md).
MD
expect broken-wrapped fail
grep -q 'docs/page.md:1: broken local link: missing-wrapped.md' "$scratch/broken-wrapped.out" ||
  { echo 'FAIL broken-wrapped: the report does not name the file and target' >&2; failures=1; }

# Brackets in prose with no link must not pair with a link paragraphs away.
fixture stray-bracket <<'MD'
A list item [x] is checked.

Later, a [real link](present.md).
MD
expect stray-bracket pass

# A broken link wrapped across a blank line is not one link: the paragraphs
# are separate, so nothing pairs across them.
fixture across-paragraphs <<'MD'
An open [bracket in one paragraph.

A closing one ](missing-across.md) in the next.
MD
expect across-paragraphs pass

# The check needs nothing but Python: with no ripgrep on PATH it still finds
# the broken wrapped link, and it never waits on standard input.
mkdir -p "$scratch/bare-bin"
for tool in bash python3 git; do ln -s "$(command -v $tool)" "$scratch/bare-bin/$tool"; done
cp -R "$scratch/broken-wrapped" "$scratch/bare"
got=0
(cd "$scratch/bare" && PATH="$scratch/bare-bin" bash "$check" >"$scratch/bare.out" 2>&1) || got=$?
[[ $got -ne 0 ]] && grep -q 'missing-wrapped.md' "$scratch/bare.out" ||
  { echo 'FAIL bare: without ripgrep the broken link was not found' >&2; failures=1; }

# An empty tree is an error, not a pass that checked nothing.
mkdir -p "$scratch/empty"
expect empty fail

[[ $failures -eq 0 ]] && echo 'PASS doc link check tests'
exit "$failures"
