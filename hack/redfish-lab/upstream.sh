#!/usr/bin/env bash
# Fetch the pinned DMTF Redfish Interface Emulator release into DEST, verified
# before extraction. Prints the provenance record as JSON.
set -euo pipefail
umask 077
dest=${1:?destination directory}
version=1.2.2
commit=5f51d8ac48e46665a2a85242434f125aa7b25172
url=https://github.com/DMTF/Redfish-Interface-Emulator/archive/refs/tags/$version.tar.gz
sha256=acee609abaedaf123b56e7ca5b2534da94bdee6fc1183dde72699c30d63de79f
mkdir -p "$dest"
archive="$dest/redfish-interface-emulator-$version.tar.gz"
[[ -f $archive ]] || curl -fsSL --max-time 120 -o "$archive" "$url"
got=$(shasum -a 256 "$archive" | cut -d' ' -f1)
[[ $got == "$sha256" ]] || { echo "upstream archive digest $got, expected $sha256" >&2; exit 1; }
rm -rf "$dest/src"
mkdir "$dest/src"
python3 - "$archive" "$dest/src" <<'PY'
import sys, tarfile
with tarfile.open(sys.argv[1]) as t:
    t.extractall(sys.argv[2], filter='data')
PY
[[ -f $dest/src/Redfish-Interface-Emulator-$version/emulator.py ]] || { echo 'unexpected archive layout' >&2; exit 1; }
printf '{"project":"DMTF Redfish-Interface-Emulator","version":"%s","commit":"%s","archive":"%s","archiveSHA256":"%s","license":"BSD-3-Clause"}\n' "$version" "$commit" "$url" "$sha256"
