package artifacts

import (
	"encoding/binary"
	"image"
	"image/color"
	"testing"
)

func TestOrientationOf(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for o := 1; o <= 8; o++ {
			exif := exifOrientation(order, o)
			jpg := withJPEGExif(encoded(t, "jpeg", halves(4, 4)), exif)
			p := withPNGExif(encoded(t, "png", halves(4, 4)), exif)
			riff := []byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00" + "\x08\x00\x00\x00\x00\x00\x00\x00\x00\x00")
			chunk := binary.LittleEndian.AppendUint32([]byte("EXIF"), uint32(len(exif)+1))
			webpWith := append(append(append(riff, chunk...), exif...), 0, 0)
			webpWithHeader := append(append(append(riff, binary.LittleEndian.AppendUint32([]byte("EXIF"), uint32(len(exif)+6))...), "Exif\x00\x00"...), exif...)
			for format, data := range map[string][]byte{"jpeg": jpg, "png": p, "webp": webpWith, "webp with a header": webpWithHeader} {
				f := format
				if f == "webp with a header" {
					f = "webp"
				}
				if got := orientationOf(f, data); got != o {
					t.Errorf("%s, %v, orientation %d: read %d", format, order, o, got)
				}
			}
		}
	}
	for format, data := range map[string][]byte{
		"jpeg": encoded(t, "jpeg", halves(4, 4)),
		"png":  encoded(t, "png", halves(4, 4)),
		"webp": encoded(t, "webp", halves(4, 4)),
		"gif":  encoded(t, "gif", halves(4, 4)),
	} {
		if got := orientationOf(format, data); got != 1 {
			t.Errorf("%s without EXIF: read %d", format, got)
		}
	}
	for _, garbage := range [][]byte{nil, {0xFF, 0xD8}, {0xFF, 0xD8, 0xFF, 0xE1, 0xFF, 0xFF}, []byte("MM\x00\x2a\xff\xff\xff\xff"), exifOrientation(binary.BigEndian, 9)} {
		for _, format := range []string{"jpeg", "png", "webp"} {
			if got := orientationOf(format, garbage); got != 1 {
				t.Errorf("%s from %q: read %d", format, garbage, got)
			}
		}
	}
}

// Each orientation puts the stored image's corners where EXIF says they belong.
func TestOrient(t *testing.T) {
	const w, h = 3, 2
	stored := image.NewRGBA(image.Rect(0, 0, w, h))
	label := func(x, y int) color.RGBA { return color.RGBA{R: uint8(x), G: uint8(y), A: 255} }
	for y := range h {
		for x := range w {
			stored.SetRGBA(x, y, label(x, y))
		}
	}
	tests := []struct {
		o                 int
		topLeft, topRight [2]int
		transposed        bool
	}{
		{1, [2]int{0, 0}, [2]int{w - 1, 0}, false},
		{2, [2]int{w - 1, 0}, [2]int{0, 0}, false},
		{3, [2]int{w - 1, h - 1}, [2]int{0, h - 1}, false},
		{4, [2]int{0, h - 1}, [2]int{w - 1, h - 1}, false},
		{5, [2]int{0, 0}, [2]int{0, h - 1}, true},
		{6, [2]int{0, h - 1}, [2]int{0, 0}, true},
		{7, [2]int{w - 1, h - 1}, [2]int{w - 1, 0}, true},
		{8, [2]int{w - 1, 0}, [2]int{w - 1, h - 1}, true},
	}
	for _, tt := range tests {
		upright := orient(stored, tt.o)
		dw, dh := w, h
		if tt.transposed {
			dw, dh = h, w
		}
		if b := upright.Bounds(); b.Dx() != dw || b.Dy() != dh {
			t.Errorf("orientation %d: %dx%d, want %dx%d", tt.o, b.Dx(), b.Dy(), dw, dh)
			continue
		}
		if got, want := upright.RGBAAt(0, 0), label(tt.topLeft[0], tt.topLeft[1]); got != want {
			t.Errorf("orientation %d: top left is stored (%d, %d), want (%d, %d)", tt.o, got.R, got.G, want.R, want.G)
		}
		if got, want := upright.RGBAAt(dw-1, 0), label(tt.topRight[0], tt.topRight[1]); got != want {
			t.Errorf("orientation %d: top right is stored (%d, %d), want (%d, %d)", tt.o, got.R, got.G, want.R, want.G)
		}
	}
}
