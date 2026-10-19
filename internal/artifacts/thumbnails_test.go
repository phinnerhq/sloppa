package artifacts

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/gen2brain/webp"

	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/storage"
)

// halves is an image whose left half is red and right half blue, to tell which way a thumbnail faces.
func halves(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.RGBA{R: 255, A: 255}
			if x >= w/2 {
				c = color.RGBA{B: 255, A: 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func encoded(t testing.TB, format string, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&b, img)
	case "jpeg":
		err = jpeg.Encode(&b, img, &jpeg.Options{Quality: 90})
	case "gif":
		err = gif.Encode(&b, img, nil)
	case "webp":
		err = webp.Encode(&b, img, webp.Options{Quality: 90})
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// exifOrientation is an EXIF (TIFF) block holding only an orientation tag.
func exifOrientation(order binary.ByteOrder, o int) []byte {
	b := make([]byte, 8+2+12+4)
	if order == binary.LittleEndian {
		copy(b, "II")
	} else {
		copy(b, "MM")
	}
	order.PutUint16(b[2:], 42)
	order.PutUint32(b[4:], 8)
	order.PutUint16(b[8:], 1)
	order.PutUint16(b[10:], 0x0112)
	order.PutUint16(b[12:], 3)
	order.PutUint32(b[14:], 1)
	order.PutUint16(b[18:], uint16(o))
	return b
}

// withJPEGExif puts an APP1 Exif segment right after a JPEG's start marker.
func withJPEGExif(jpg, exif []byte) []byte {
	payload := append([]byte("Exif\x00\x00"), exif...)
	segment := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(segment[2:], uint16(len(payload)+2))
	out := append([]byte{}, jpg[:2]...)
	out = append(out, segment...)
	out = append(out, payload...)
	return append(out, jpg[2:]...)
}

func pngChunk(kind string, data []byte) []byte {
	chunk := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	chunk = append(chunk, kind...)
	chunk = append(chunk, data...)
	return binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(append([]byte(kind), data...)))
}

// withPNGExif puts an eXIf chunk right after a PNG's header.
func withPNGExif(p, exif []byte) []byte {
	const afterIHDR = 8 + 8 + 13 + 4
	out := append([]byte{}, p[:afterIHDR]...)
	out = append(out, pngChunk("eXIf", exif)...)
	return append(out, p[afterIHDR:]...)
}

type thumbStore struct {
	storage.Store
	reads   atomic.Int32
	release chan struct{}
}

func (s *thumbStore) Read(ctx context.Context, id, rangeHeader string) (*storage.Object, error) {
	s.reads.Add(1)
	if s.release != nil {
		<-s.release
	}
	return s.Store.Read(ctx, id, rangeHeader)
}

func newThumbs(t *testing.T) (*thumbnails, *thumbStore) {
	t.Helper()
	dir := t.TempDir()
	store := &thumbStore{Store: newFsStore(t, filepath.Join(dir, "files"))}
	thumbs, err := newThumbnails(filepath.Join(dir, "thumbs"), store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { thumbs.close() })
	return thumbs, store
}

func putFile(t *testing.T, s storage.Store, contentType string, data []byte) string {
	t.Helper()
	a, err := s.Put(t.Context(), storage.PutInput{Name: "f", ContentType: contentType, Body: bytes.NewReader(data), Size: int64(len(data)), Collection: "c"})
	if err != nil {
		t.Fatal(err)
	}
	return a.ID
}

func decodeThumb(t *testing.T, thumb []byte) image.Image {
	t.Helper()
	img, format, err := image.Decode(bytes.NewReader(thumb))
	if err != nil || format != "webp" {
		t.Fatalf("thumbnail is not a WebP: %v (%s)", err, format)
	}
	return img
}

func near(c color.Color, want color.RGBA) bool {
	r, g, b, _ := c.RGBA()
	d := func(a uint32, b uint8) bool { return int(a>>8)-int(b) < 40 && int(b)-int(a>>8) < 40 }
	return d(r, want.R) && d(g, want.G) && d(b, want.B)
}

var (
	red  = color.RGBA{R: 255, A: 255}
	blue = color.RGBA{B: 255, A: 255}
)

func TestThumbnailSizes(t *testing.T) {
	thumbs, store := newThumbs(t)
	tests := []struct {
		format, contentType string
		w, h                int
		wantW, wantH        int
	}{
		{"png", "image/png", 1200, 600, 480, 240},
		{"jpeg", "image/jpeg", 600, 900, 320, 480},
		{"gif", "image/gif", 100, 50, 100, 50},
		{"webp", "image/webp; charset=binary", 1000, 500, 480, 240},
		{"png", "image/png", 4000, 3, 480, 1},
	}
	for _, tt := range tests {
		id := putFile(t, store, tt.contentType, encoded(t, tt.format, halves(tt.w, tt.h)))
		thumb, err := thumbs.get(t.Context(), id)
		if err != nil {
			t.Errorf("%s %dx%d: %v", tt.format, tt.w, tt.h, err)
			continue
		}
		img := decodeThumb(t, thumb)
		if b := img.Bounds(); b.Dx() != tt.wantW || b.Dy() != tt.wantH {
			t.Errorf("%s %dx%d: thumbnail is %dx%d, want %dx%d", tt.format, tt.w, tt.h, b.Dx(), b.Dy(), tt.wantW, tt.wantH)
		}
		b := img.Bounds()
		if !near(img.At(b.Min.X, b.Min.Y), red) || !near(img.At(b.Max.X-1, b.Min.Y), blue) {
			t.Errorf("%s: corners are %v and %v, want red and blue", tt.format, img.At(b.Min.X, b.Min.Y), img.At(b.Max.X-1, b.Min.Y))
		}
	}
}

// A photo taken sideways comes out upright, as a browser shows the original.
func TestThumbnailsAreUpright(t *testing.T) {
	thumbs, store := newThumbs(t)
	tests := []struct {
		name string
		data []byte
	}{
		{"jpeg", withJPEGExif(encoded(t, "jpeg", halves(600, 300)), exifOrientation(binary.BigEndian, 6))},
		{"png", withPNGExif(encoded(t, "png", halves(600, 300)), exifOrientation(binary.LittleEndian, 6))},
	}
	for _, tt := range tests {
		id := putFile(t, store, "image/"+tt.name, tt.data)
		thumb, err := thumbs.get(t.Context(), id)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		img := decodeThumb(t, thumb)
		b := img.Bounds()
		if b.Dx() != 240 || b.Dy() != 480 {
			t.Errorf("%s: thumbnail is %dx%d, want 240x480", tt.name, b.Dx(), b.Dy())
		}
		// Turned clockwise, the left half is on top.
		if !near(img.At(b.Min.X+5, b.Min.Y+5), red) || !near(img.At(b.Min.X+5, b.Max.Y-5), blue) {
			t.Errorf("%s: top %v, bottom %v, want red over blue", tt.name, img.At(5, 5), img.At(5, b.Max.Y-5))
		}
	}
}

// pngHeader is a PNG with nothing but a header: enough for DecodeConfig, not for Decode.
func pngHeader(w, h uint32, depth, colorType, interlace byte) []byte {
	ihdr := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, w), h)
	ihdr = append(ihdr, depth, colorType, 0, 0, interlace)
	return append([]byte("\x89PNG\r\n\x1a\n"), pngChunk("IHDR", ihdr)...)
}

// jpegHeader is a JPEG with nothing but a JFIF marker, which ends DecodeConfig at the frame header,
// and a frame header of the given kind (0xC0 baseline, 0xC2 progressive) and component count.
func jpegHeader(sof byte, w, h uint16, components int) []byte {
	frame := []byte{8, byte(h >> 8), byte(h), byte(w >> 8), byte(w), byte(components)}
	for i := range components {
		frame = append(frame, byte(i+1), 0x11, 0)
	}
	b := append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 16}, "JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00"...)
	b = append(b, 0xFF, sof)
	b = binary.BigEndian.AppendUint16(b, uint16(len(frame)+2))
	return append(b, frame...)
}

// withJunk puts bytes that aren't a marker between a JPEG's header segments and its frame header.
func withJunk(jpeg []byte) []byte {
	sof := 2 + 2 + 16
	return append(append(append([]byte{}, jpeg[:sof]...), 0x00, 0x13, 0x37), jpeg[sof:]...)
}

// truncated is data cut short of its last chunk's end, as a header that lies about its size reads.
func truncated(data []byte) []byte { return data[:len(data)-1] }

// tiffImage is the start of a little-endian TIFF: no decoder reads it.
var tiffImage = []byte("II*\x00\x08\x00\x00\x00\x00\x00")

func TestNoThumbnail(t *testing.T) {
	thumbs, store := newThumbs(t)
	tests := []struct {
		why, contentType string
		data             []byte
	}{
		{"not an image", "text/plain", []byte("hello")},
		{"a format without a decoder", "image/avif", []byte("....ftypavif")},
		{"a vector image", "image/svg+xml", []byte("<svg/>")},
		{"a broken image", "image/png", []byte("\x89PNG\r\n\x1a\nnope")},
		{"too many pixels", "image/png", pngHeader(10000, 10000, 8, 6, 0)},
		{"a TIFF", "image/tiff", tiffImage},
		{"a TIFF passed off as a PNG", "image/png", tiffImage},
	}
	for _, tt := range tests {
		id := putFile(t, store, tt.contentType, tt.data)
		if _, err := thumbs.get(t.Context(), id); !errors.Is(err, ErrNoThumbnail) {
			t.Errorf("%s: %v, want ErrNoThumbnail", tt.why, err)
		}
	}
	for _, id := range []string{"00000000-0000-4000-8000-000000000000", "../../etc/passwd"} {
		_, err := thumbs.get(t.Context(), id)
		var e *api.Error
		if !errors.As(err, &e) || e.Code != "artifact_not_found" {
			t.Errorf("%s: %v, want ArtifactNotFound", id, err)
		}
	}
}

// What decoding would take is told from the header, and too much is refused before decoding: these
// images are nothing but a header, so decoding them would fail otherwise.
func TestDecodeBudget(t *testing.T) {
	tests := []struct {
		why  string
		data []byte
		// perPixel is the expected estimate.
		perPixel int64
	}{
		{"8-bit RGBA", pngHeader(1000, 1000, 8, 6, 0), 4},
		{"16-bit RGBA", pngHeader(1000, 1000, 16, 6, 0), 8},
		{"16-bit gray", pngHeader(1000, 1000, 16, 0, 0), 8},
		{"paletted", append(pngHeader(1000, 1000, 8, 3, 0), append(pngChunk("PLTE", []byte{0, 0, 0}), pngChunk("IDAT", nil)...)...), 4},
		{"interlaced", pngHeader(1000, 1000, 8, 6, 1), 8},
		{"interlaced 16-bit", pngHeader(1000, 1000, 16, 6, 1), 16},
		{"baseline JPEG", jpegHeader(0xC0, 1000, 1000, 3), 4},
		{"baseline CMYK JPEG", jpegHeader(0xC0, 1000, 1000, 4), 8},
		{"progressive JPEG", jpegHeader(0xC2, 1000, 1000, 3), 16},
		{"progressive CMYK JPEG", jpegHeader(0xC2, 1000, 1000, 4), 24},
		// Go's decoder skips junk before a segment; the estimate mustn't take it for a baseline one.
		{"progressive JPEG behind junk", withJunk(jpegHeader(0xC2, 1000, 1000, 3)), 16},
		{"baseline JPEG behind junk", withJunk(jpegHeader(0xC0, 1000, 1000, 3)), 16},
		{"lossy WebP", encoded(t, "webp", halves(1000, 1000)), 8},
		{"lossy WebP cut short", truncated(encoded(t, "webp", halves(1000, 1000))), 26},
	}
	for _, tt := range tests {
		cfg, format, err := image.DecodeConfig(bytes.NewReader(tt.data))
		if err != nil {
			t.Fatalf("%s: %v", tt.why, err)
		}
		if got, want := decodedSize(format, cfg, tt.data), tt.perPixel*1000*1000; got != want {
			t.Errorf("%s: estimated %d bytes, want %d", tt.why, got, want)
		}
	}

	refused := map[string][]byte{
		// 28 MP, which the old flat cap of 50 MP let through, at 8 bytes a pixel.
		"16-bit":      pngHeader(4000, 7000, 16, 6, 0),
		"interlaced":  pngHeader(5000, 6000, 8, 2, 1),
		"progressive": jpegHeader(0xC2, 4000, 4000, 3),
	}
	for why, data := range refused {
		if _, err := shrink(data); err == nil || !strings.Contains(err.Error(), "MiB, over 192") {
			t.Errorf("%s: %v, want refused for its size", why, err)
		}
	}
	// 50 MP at 4 bytes a pixel still fits: decoding is tried, and fails for want of pixels.
	if _, err := shrink(pngHeader(5000, 10000, 8, 6, 0)); err == nil || strings.Contains(err.Error(), "MiB") {
		t.Errorf("50 MP: %v, want a decoding error", err)
	}
}

// Made once, then served from the cache, even after the original is gone; deleting the artifact
// deletes it.
func TestThumbnailsAreCached(t *testing.T) {
	thumbs, store := newThumbs(t)
	id := putFile(t, store, "image/png", encoded(t, "png", halves(10, 10)))
	var wg sync.WaitGroup
	results := make([][]byte, 8)
	for i := range results {
		wg.Go(func() {
			thumb, err := thumbs.get(t.Context(), id)
			if err != nil {
				t.Error(err)
			}
			results[i] = thumb
		})
	}
	wg.Wait()
	if n := store.reads.Load(); n != 1 {
		t.Errorf("read the original %d times, want once", n)
	}
	for _, r := range results {
		if !bytes.Equal(r, results[0]) {
			t.Error("requests got different thumbnails")
		}
	}
	if err := store.Store.Remove(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if cached, err := thumbs.get(t.Context(), id); err != nil || !bytes.Equal(cached, results[0]) {
		t.Errorf("cached thumbnail: %v", err)
	}
	thumbs.remove(id)
	if _, err := thumbs.root.Stat(fileOf(id)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("thumbnail left after remove: %v", err)
	}
	if entries, _ := os.ReadDir(thumbs.root.Name()); len(entries) != 0 {
		t.Errorf("left %d files in the thumbnail dir", len(entries))
	}
}

// A request that gives up doesn't waste the work: the thumbnail is still made, for the next one.
func TestThumbnailsOutliveTheRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		thumbs, store := newThumbs(t)
		id := putFile(t, store, "image/png", encoded(t, "png", halves(10, 10)))
		store.release = make(chan struct{})
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error)
		go func() {
			_, err := thumbs.get(ctx, id)
			done <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("get: %v, want context.Canceled", err)
		}
		close(store.release)
		synctest.Wait()
		if _, err := thumbs.root.Stat(fileOf(id)); err != nil {
			t.Errorf("not cached: %v", err)
		}
	})
}

// heldStore holds a Read once it has opened the original, until released.
type heldStore struct {
	storage.Store
	opened, release chan struct{}
}

func (s *heldStore) Read(ctx context.Context, id, rangeHeader string) (*storage.Object, error) {
	o, err := s.Store.Read(ctx, id, rangeHeader)
	if err == nil {
		close(s.opened)
		<-s.release
	}
	return o, err
}

// A thumbnail being made of an artifact as it's deleted isn't cached: it would be served after the
// delete, forever.
func TestThumbnailsOfDeletedArtifactsArentCached(t *testing.T) {
	dir := t.TempDir()
	store := &heldStore{Store: newFsStore(t, filepath.Join(dir, "files")), opened: make(chan struct{}), release: make(chan struct{})}
	s := open(t, dir, store)
	id := putFile(t, store, "image/png", encoded(t, "png", halves(10, 10)))
	made := make(chan error, 1)
	go func() {
		_, err := s.Thumbnail(t.Context(), id)
		made <- err
	}()
	<-store.opened
	if err := s.Remove(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	close(store.release)
	if err := <-made; apiError(t, err).Code != "artifact_not_found" {
		t.Errorf("thumbnail made as it was deleted: %v", err)
	}
	if entries, _ := os.ReadDir(s.thumbs.root.Name()); len(entries) != 0 {
		t.Errorf("left %d files in the thumbnail dir", len(entries))
	}
	if _, err := s.Thumbnail(t.Context(), id); apiError(t, err).Code != "artifact_not_found" {
		t.Errorf("thumbnail after the delete: %v", err)
	}
}

// Two originals are decoded at a time, at most.
func TestThumbnailsTakeTurns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		thumbs, store := newThumbs(t)
		var ids []string
		for range 5 {
			ids = append(ids, putFile(t, store, "image/png", encoded(t, "png", halves(10, 10))))
		}
		store.release = make(chan struct{})
		var wg sync.WaitGroup
		for _, id := range ids {
			wg.Go(func() {
				if _, err := thumbs.get(t.Context(), id); err != nil {
					t.Error(err)
				}
			})
		}
		synctest.Wait()
		if n := store.reads.Load(); n != decoders {
			t.Errorf("%d reads at once, want %d", n, decoders)
		}
		close(store.release)
		wg.Wait()
		if n := store.reads.Load(); n != 5 {
			t.Errorf("%d reads in all, want 5", n)
		}
	})
}

// Thumbnails waiting for a decoder are bounded: past that, a request is told to come back later,
// and nothing is read.
func TestThumbnailQueueIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		thumbs, store := newThumbs(t)
		thumbs.queue = make(chan struct{}, decoders+1)
		var ids []string
		for range decoders + 2 {
			ids = append(ids, putFile(t, store, "image/png", encoded(t, "png", halves(10, 10))))
		}
		store.release = make(chan struct{})
		var wg sync.WaitGroup
		for _, id := range ids[:decoders+1] {
			wg.Go(func() {
				if _, err := thumbs.get(t.Context(), id); err != nil {
					t.Error(err)
				}
			})
		}
		synctest.Wait()
		_, err := thumbs.get(t.Context(), ids[decoders+1])
		var e *api.Error
		if !errors.As(err, &e) || e.Code != "too_many_attempts" || e.RetryAfter != 1 {
			t.Errorf("over the queue: %v, want TooManyAttempts", err)
		}
		if n := store.reads.Load(); n != decoders {
			t.Errorf("%d reads, want %d", n, decoders)
		}
		close(store.release)
		wg.Wait()
		// Once the queue drains, it's made like any other.
		if _, err := thumbs.get(t.Context(), ids[decoders+1]); err != nil {
			t.Errorf("after the queue drained: %v", err)
		}
	})
}

func BenchmarkShrinkPhoto(b *testing.B) {
	photo := encoded(b, "jpeg", halves(4000, 3000))
	for b.Loop() {
		if _, err := shrink(photo); err != nil {
			b.Fatal(err)
		}
	}
}
