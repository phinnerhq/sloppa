# Build mascot stickers

Save the source photos from [Credits.tsx](../../apps/web/src/components/Credits.tsx) as `stare.jpg`, `hiss.jpg`, and `loaf.jpg` in `scripts/mascot/raw`. That file also holds their attribution and licenses.

From the repo root:

```sh
cd scripts/mascot
mkdir -p cut fx
python3 -m venv .venv && .venv/bin/pip install pillow "rembg[cpu]"
.venv/bin/python cutout.py isnet-general-use cut raw/stare.jpg raw/hiss.jpg raw/loaf.jpg
.venv/bin/python effects.py cut fx
.venv/bin/python export.py fx ../../apps/web/public/floppa
```
