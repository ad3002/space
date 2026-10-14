#!/usr/bin/env bash
# Render UI pages with headless Chrome for visual checks.
#   tools/screenshots.sh http://127.0.0.1:18377 OUTDIR [light|dark] route...
# Also dumps the DOM (with JS errors surfaced by the page) as OUTDIR/<name>.html.
set -euo pipefail
base="$1"; out="$2"; theme="${3:-light}"; shift 3
mkdir -p "$out"
flags=(--headless=new --disable-gpu --no-sandbox --hide-scrollbars --window-size=1360,2400 --virtual-time-budget=8000)
[ "$theme" = dark ] && flags+=(--force-dark-mode --blink-settings=preferredColorScheme=0)
for route in "$@"; do
  name="$(printf '%s' "$route" | tr -c 'a-zA-Z0-9' '_' | sed 's/_*$//')_${theme}"
  google-chrome "${flags[@]}" --screenshot="$out/$name.png" "$base/#/$route" >/dev/null 2>&1
  google-chrome "${flags[@]}" --dump-dom "$base/#/$route" > "$out/$name.html" 2>/dev/null
  echo "$out/$name.png"
done
