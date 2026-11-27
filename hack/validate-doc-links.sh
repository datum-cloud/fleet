#!/usr/bin/env bash
set -euo pipefail

# Without rg the scan below would read nothing and pass.
command -v rg >/dev/null || { echo 'validate-doc-links: rg (ripgrep) is required' >&2; exit 1; }
cd "$(dirname "$0")/.."
# The explicit path matters: given none, rg searches stdin whenever stdin is
# not a terminal, which in CI or a pipe reads no file and finds no link.
links=$(rg -n -o --replace '$1' '\[[^]]+\]\(([^ )]+)' --glob '*.md' . </dev/null)
[[ -n $links ]] || { echo 'validate-doc-links: found no Markdown links to check' >&2; exit 1; }
failed=0 checked=0
while IFS=: read -r file _ target; do
  target=${target%%#*}
  case "$target" in
    ''|http://*|https://*|mailto:*) continue ;;
  esac
  checked=$((checked + 1))
  if [[ ! -e "$(dirname "$file")/$target" ]]; then
    echo "$file: broken local link: $target" >&2
    failed=1
  fi
done <<<"$links"
echo "validate-doc-links: checked $checked local links"
exit "$failed"
