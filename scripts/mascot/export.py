import sys
from PIL import Image
src, out = sys.argv[1], sys.argv[2]
for name, size in [("stare-sticker", 320), ("hiss-sticker", 360), ("loaf-sticker", 420)]:
    im = Image.open(f"{src}/{name}.png"); im.thumbnail((size, size), Image.Resampling.LANCZOS)
    im.save(f"{out}/{name.replace('-sticker', '')}.webp", "WEBP", quality=88, method=6)
fav = Image.open(f"{src}/hiss-sticker.png"); fav.thumbnail((64, 64), Image.Resampling.LANCZOS)
sq = Image.new("RGBA", (64, 64)); sq.alpha_composite(fav, ((64 - fav.width) // 2, (64 - fav.height) // 2)); sq.save(f"{out}/favicon.png")
