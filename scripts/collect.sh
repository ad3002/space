#!/usr/bin/env bash
# Take a disk-usage snapshot of one or more paths for the `space` viewer.
#
# Needs only: sudo ncdu (to read everything), gzip, df, getent.
# Run it by hand inside tmux/screen — a scan of tens of millions of files
# takes a long time and sudo will ask for your password once:
#
#   tmux new -s scan
#   ./scripts/collect.sh /data /home            # writes into ./snapshots
#   OUT=/path/to/snapshots ./scripts/collect.sh /data
#
# For each path it writes (<id> = <host>__<label>__<timestamp>):
#   <id>.ncdu.json.gz   full tree with owner/mode/mtime  (sudo ncdu -x -e -o -)
#   <id>.df             df -B1 -P <path>   (disk capacity, no sudo)
#   <id>.passwd/.group  getent passwd/group (uid -> name)
# While the scan runs the export is named <id>.ncdu.json.gz.part.
#
# MODE=file makes ncdu write to a file itself instead of stdout (use it if
# `sudo ncdu -o -` misbehaves on your sudo setup); the root-owned result is
# then handed back to you with `sudo chown` and compressed.
set -euo pipefail

OUT="${OUT:-$(cd "$(dirname "$0")/.." && pwd)/snapshots}"
MODE="${MODE:-pipe}"
NCDU="${NCDU:-ncdu}"
SUDO="${SUDO-sudo}"      # SUDO= ./collect.sh DIR  scans without sudo (dirs you can read)
EXTRA="${NCDU_ARGS:-}"   # e.g. NCDU_ARGS="--exclude .snapshot"

if [ $# -eq 0 ]; then
  echo "usage: $0 PATH [PATH...]" >&2
  exit 2
fi
mkdir -p "$OUT"
host="$(hostname -s)"

for target in "$@"; do
  target="$(cd "$target" && pwd -P)"
  label="$(printf '%s' "$target" | sed 's#^/##; s#/#-#g')"
  [ -n "$label" ] || label="root"
  ts="$(date +%Y%m%d-%H%M%S)"
  id="${host}__${label}__${ts}"
  base="$OUT/$id"

  echo "== $target -> $base.ncdu.json.gz"
  df -B1 -P "$target" > "$base.df"
  getent passwd > "$base.passwd" || echo "warning: getent passwd failed; owners will show as numeric uids" >&2
  getent group > "$base.group" || true

  start=$(date +%s)
  # shellcheck disable=SC2086
  if [ "$MODE" = file ]; then
    raw="$base.ncdu.json"
    $SUDO "$NCDU" -x -e -0 $EXTRA -o "$raw.part" "$target"
    [ -z "$SUDO" ] || $SUDO chown "$(id -u):$(id -g)" "$raw.part"
    gzip -1 -c "$raw.part" > "$base.ncdu.json.gz.part"
    rm -f "$raw.part"
  else
    $SUDO "$NCDU" -x -e -0 $EXTRA -o - "$target" | gzip -1 > "$base.ncdu.json.gz.part"
  fi
  # Refuse to publish a broken export: the viewer would reject it anyway.
  if ! gzip -t "$base.ncdu.json.gz.part"; then
    echo "ERROR: $base.ncdu.json.gz.part is not a valid gzip stream; keeping .part for inspection" >&2
    exit 1
  fi
  mv "$base.ncdu.json.gz.part" "$base.ncdu.json.gz"
  end=$(date +%s)
  echo "   done in $(( (end - start) / 60 )) min, $(du -h "$base.ncdu.json.gz" | cut -f1) compressed"
done
echo "Snapshots are in $OUT — the viewer picks them up automatically."
