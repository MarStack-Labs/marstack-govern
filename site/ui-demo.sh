#!/usr/bin/env bash
set -euo pipefail

BASE="${BASE:-http://127.0.0.1:8099}"
OUT_GIF="${OUT_GIF:-site/ui-demo.gif}"
WORK="${WORK:-/tmp/margov-ui}"
WIDTH="${WIDTH:-1360}"
HEIGHT="${HEIGHT:-860}"

command -v ffmpeg >/dev/null || { echo "ffmpeg is required to make the gif" >&2; exit 1; }
command -v node >/dev/null || { echo "node is required to drive the browser" >&2; exit 1; }

if ! curl -fsS -o /dev/null "$BASE/healthz"; then
  echo "nothing is serving at $BASE — start margov first" >&2
  exit 1
fi

rm -rf "$WORK"
mkdir -p "$WORK/video"

VIDEO_DIR="$WORK/video" BASE="$BASE" WIDTH="$WIDTH" HEIGHT="$HEIGHT" \
  node site/ui-demo.mjs

WEBM=$(find "$WORK/video" -name '*.webm' | head -1)
if [ -z "$WEBM" ]; then
  echo "the browser recorded no video" >&2
  exit 1
fi

ffmpeg -hide_banner -loglevel error -y -i "$WEBM" \
  -vf "fps=6,scale=900:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=64[p];[b][p]paletteuse=dither=bayer:bayer_scale=4" \
  -loop 0 "$OUT_GIF"

echo "wrote $OUT_GIF"
