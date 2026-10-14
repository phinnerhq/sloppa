"""Turn a rembg cutout into stickers: clean alpha, grade, outline, drop shadow."""
import io, sys
from PIL import Image, ImageChops, ImageEnhance, ImageFilter

INK = (29, 36, 51, 255)  # panel outline colour of the UI


def main_component(mask, seed):
    """Keep only the blob containing `seed` (drops whisker crumbs and background islands)."""
    from PIL import ImageDraw
    marked = mask.copy()
    ImageDraw.floodfill(marked, seed, 128)
    return marked.point(lambda v: 255 if v == 128 else 0)


def clean_alpha(im, crop_bottom=None, seed=None):
    r, g, b, a = im.split()
    a = a.point(lambda v: 255 if v > 140 else 0).filter(ImageFilter.MedianFilter(7))
    if seed:
        a = main_component(a, seed)
    a = a.filter(ImageFilter.GaussianBlur(0.8))
    im = Image.merge("RGBA", (r, g, b, a))
    if crop_bottom:
        im = im.crop((0, 0, im.width, crop_bottom))
    return im.crop(im.getbbox())


def grade(im, sat=1.35, con=1.12, warm=True):
    rgb, a = im.convert("RGB"), im.getchannel("A")
    rgb = ImageEnhance.Color(rgb).enhance(sat)
    rgb = ImageEnhance.Contrast(rgb).enhance(con)
    rgb = rgb.filter(ImageFilter.UnsharpMask(radius=2, percent=80, threshold=2))
    if warm:  # nudge towards the cosy caracal tawny
        r, g, b = rgb.split()
        rgb = Image.merge("RGB", (r.point(lambda v: min(255, int(v * 1.06))), g, b.point(lambda v: int(v * 0.92))))
    out = rgb.convert("RGBA"); out.putalpha(a); return out


def toon(im, colors=20):
    a = im.getchannel("A")
    rgb = im.convert("RGB").filter(ImageFilter.SMOOTH_MORE)
    rgb = rgb.quantize(colors=colors, method=Image.Quantize.MEDIANCUT, dither=Image.Dither.NONE).convert("RGB")
    out = rgb.convert("RGBA"); out.putalpha(a); return out


def deep_fry(im):
    a = im.getchannel("A")
    rgb = ImageEnhance.Color(im.convert("RGB")).enhance(2.6)
    rgb = ImageEnhance.Contrast(rgb).enhance(1.7)
    rgb = ImageEnhance.Sharpness(rgb).enhance(6)
    for q in (12, 9):
        buf = io.BytesIO(); rgb.save(buf, "JPEG", quality=q); rgb = Image.open(io.BytesIO(buf.getvalue())).convert("RGB")
    r, g, b = rgb.split()
    rgb = Image.merge("RGB", (r.point(lambda v: min(255, int(v * 1.25))), g, b.point(lambda v: int(v * 0.7))))
    out = rgb.convert("RGBA"); out.putalpha(a); return out


def dilate(alpha, px):
    return alpha.filter(ImageFilter.MaxFilter(px * 2 + 1)) if px > 0 else alpha


def sticker(im, size, white=10, ink=5, shadow=10):
    im = im.copy(); im.thumbnail((size, size), Image.Resampling.LANCZOS)
    pad = white + ink + shadow + 4
    canvas = Image.new("RGBA", (im.width + pad * 2, im.height + pad * 2), (0, 0, 0, 0))
    base_a = Image.new("L", canvas.size, 0); base_a.paste(im.getchannel("A"), (pad, pad))
    white_a = dilate(base_a, white)
    ink_a = dilate(white_a, ink).filter(ImageFilter.GaussianBlur(0.6))
    shadow_a = ImageChops.offset(ink_a, 0, shadow)
    fill = lambda rgb, a: Image.merge("RGBA", (*Image.new("RGB", canvas.size, rgb).split(), a))
    canvas.alpha_composite(fill(INK[:3], shadow_a.point(lambda v: int(v * 0.35))))
    canvas.alpha_composite(fill(INK[:3], ink_a))
    canvas.alpha_composite(fill((255, 250, 242), white_a))
    layer = Image.new("RGBA", canvas.size, (0, 0, 0, 0)); layer.paste(im, (pad, pad))
    canvas.alpha_composite(layer)
    return canvas.crop(canvas.getbbox())


if __name__ == "__main__":
    src_dir, out_dir = sys.argv[1], sys.argv[2]
    # name: (bottom crop, a point inside the cat)
    crops = {"stare": (760, (520, 500)), "hiss": (960, (560, 500)), "loaf": (None, (700, 600))}
    for name, (crop, seed) in crops.items():
        cut = clean_alpha(Image.open(f"{src_dir}/{name}.png").convert("RGBA"), crop, seed)
        graded = grade(cut)
        sticker(graded, 512).save(f"{out_dir}/{name}-sticker.png")
        sticker(deep_fry(cut), 512).save(f"{out_dir}/{name}-fried.png")
        print("done", name)
