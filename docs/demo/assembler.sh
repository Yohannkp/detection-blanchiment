#!/usr/bin/env bash
# Assemble les images capturées par enregistrer_demo.js, en respectant leurs durées réelles :
#   demo.mp4 (H.264, 1600×1000, pour une vidéo nette) et demo.gif (960 px, pour le README).
set -euo pipefail
cd "$(dirname "$0")"

"${PY:-python3}" - <<'PY'
import json, pathlib
d = pathlib.Path("images")
durees = json.loads((d / "durees.json").read_text())
images = sorted(d.glob("*.jpg"))
lignes = []
for img, t in zip(images, durees):
    lignes += [f"file '{img.name}'", f"duration {t:.3f}"]
lignes.append(f"file '{images[-1].name}'")  # le démultiplexeur ignore la durée de la dernière image
(d / "liste.txt").write_text("\n".join(lignes))
PY

ffmpeg -loglevel error -y -f concat -safe 0 -i images/liste.txt -vf "fps=30,format=yuv420p" \
  -c:v libx264 -preset slow -crf 20 -movflags +faststart demo.mp4
# GIF : palette calculée sur toute la vidéo (256 couleurs au plus).
FILTRE="fps=${IPS:-10},scale=${LARGEUR:-960}:-1:flags=lanczos"
ffmpeg -loglevel error -y -f concat -safe 0 -i images/liste.txt -vf "$FILTRE,palettegen=max_colors=160:stats_mode=diff" images/palette.png
ffmpeg -loglevel error -y -f concat -safe 0 -i images/liste.txt -i images/palette.png \
  -lavfi "$FILTRE [x]; [x][1:v] paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle" -loop 0 demo.gif
ls -la demo.mp4 demo.gif
