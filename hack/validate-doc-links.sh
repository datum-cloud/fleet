#!/usr/bin/env bash
# Fails on any Markdown link to a local file that does not exist. Link text
# may wrap across lines, but not across a blank line. Needs only Python, so it
# checks the same links wherever it runs, and it never reads standard input.
set -euo pipefail
exec python3 - "$@" <<'PY'
import os, re, subprocess, sys

# [text](target): the text may wrap onto following lines, never past a
# blank line, and holds no other brackets.
LINK = re.compile(r'\[(?:[^\[\]\n]|\n(?![ \t]*\n))+\]\(([^\s)]+)')


def markdown_files():
    """Tracked and untracked Markdown files that Git does not ignore, or every
    Markdown file outside hidden directories when this is not a Git checkout."""
    listing = subprocess.run(['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard', '--', '*.md'],
                             capture_output=True, text=True)
    if listing.returncode == 0:
        return sorted({p for p in listing.stdout.split('\0') if p and os.path.isfile(p)})
    found = []
    for directory, subdirectories, files in os.walk('.'):
        subdirectories[:] = [d for d in subdirectories if not d.startswith('.')]
        found += [os.path.relpath(os.path.join(directory, f)) for f in files if f.endswith('.md')]
    return sorted(found)


failed = False
files = markdown_files()
for path in files:
    with open(path, encoding='utf-8') as f:
        text = f.read()
    for match in LINK.finditer(text):
        target = match.group(1).split('#', 1)[0]
        if not target or target.startswith(('http://', 'https://', 'mailto:')):
            continue
        if not os.path.exists(os.path.join(os.path.dirname(path), target)):
            print('%s:%d: broken local link: %s' % (path, text.count('\n', 0, match.start()) + 1, target), file=sys.stderr)
            failed = True
if not files:
    print('no Markdown files found; nothing was checked', file=sys.stderr)
    failed = True
sys.exit(1 if failed else 0)
PY
