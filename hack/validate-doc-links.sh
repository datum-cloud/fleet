#!/usr/bin/env bash
set -euo pipefail

failed=0
while IFS=: read -r file _ target; do
  target=${target%%#*}
  case "$target" in
    ''|http://*|https://*|mailto:*) continue ;;
  esac
  if [[ ! -e "$(dirname "$file")/$target" ]]; then
    echo "$file: broken local link: $target" >&2
    failed=1
  fi
done < <(rg -n -o --replace '$1' '\[[^]]+\]\(([^ )]+)' --glob '*.md')
exit "$failed"
