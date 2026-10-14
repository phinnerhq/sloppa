# Mascot

Turns caracal photos into the stickers in `apps/web/public/floppa`.

```sh
python3 -m venv .venv && .venv/bin/pip install pillow "rembg[cpu]"
.venv/bin/python cutout.py isnet-general-use cut raw/stare.jpg raw/hiss.jpg raw/loaf.jpg
.venv/bin/python effects.py cut fx
.venv/bin/python export.py fx ../../apps/web/public/floppa
```

Photos (credited on the app's `/credits` page):

- `stare`: [Caracal Portrait](<https://commons.wikimedia.org/wiki/File:Caracal_Portrait_(7476164046).jpg>), Charles Barilleaux, CC BY 2.0
- `hiss`: [Caracal caracal at the Zoo](https://commons.wikimedia.org/wiki/File:Caracal_caracal_at_the_Zoo.jpg), ZhanserikKT, CC BY 4.0
- `loaf`: [Gfp-caracal](https://commons.wikimedia.org/wiki/File:Gfp-caracal.jpg), Yinan Chen, public domain
