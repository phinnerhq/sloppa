import sys
from PIL import Image
from rembg import new_session, remove
session = new_session(sys.argv[1])
for src in sys.argv[3:]:
    out = remove(Image.open(src).convert("RGB"), session=session, alpha_matting=True,
                 alpha_matting_foreground_threshold=240, alpha_matting_background_threshold=15)
    name = src.rsplit("/", 1)[-1].rsplit(".", 1)[0]
    out.save(f"{sys.argv[2]}/{name}.png")
    print("cut", name, out.getbbox())
