package artifacts

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"strings"
	"sync"

	"github.com/gen2brain/webp"
	"golang.org/x/image/draw"
	"golang.org/x/sync/singleflight"

	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/storage"
)

// raster is what the decoders imported above read. GIFs keep their first frame. AVIF isn't here: no
// decoder for it is both cgo-free and small, and neither is TIFF, whose decoder's buffers aren't
// bounded by the image's size; the dashboard shows those originals.
var raster = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
	"image/gif":  true,
}

func hasThumbnail(contentType string) bool {
	mediaType, _, _ := strings.Cut(contentType, ";")
	return raster[strings.ToLower(strings.TrimSpace(mediaType))]
}

const (
	// thumbSize is the bounding box, in pixels: a grid slot is under 200 CSS px wide, so this stays
	// sharp up to 2.5x.
	thumbSize = 480
	// decodeBudget caps what decoding one image may take, as decodedSize estimates it: a 50 MP
	// screenshot, at 4 bytes per pixel, still fits.
	decodeBudget = 192 << 20
	// decoders is how many originals are decoded at once: each is held whole in memory, so a page of
	// fresh uploads takes turns.
	decoders = 2
	// queued is how many more thumbnails may wait for a decoder. Waiting holds no original, only a
	// goroutine, but it carries on after the request gives up: past this, a flood of distinct ids is
	// told to come back later instead of piling up.
	queued = 256
)

// thumbnails are small WebP previews for the inventory grid, so a page of screenshots doesn't
// download every original. Made on first request and kept under DATA_DIR/thumbs: like the index, they
// derive from storage and are safe to delete.
type thumbnails struct {
	// root is the thumbnail dir, through which a thumbnail being read can be deleted on Windows too.
	root     *os.Root
	store    storage.Store
	making   singleflight.Group
	decoding chan struct{}
	// queue holds a place for each thumbnail being made or waiting to be, decoders + queued at most.
	queue chan struct{}

	// mu orders caching a thumbnail against deleting it. A thumbnail being made has an entry in
	// pending, which a delete sets: it was made of an original that's gone, so it isn't cached, or it
	// would be served after the delete, forever.
	mu      sync.Mutex
	pending map[string]bool
}

func newThumbnails(dir string, store storage.Store) (*thumbnails, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("make the thumbnail dir: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open the thumbnail dir: %w", err)
	}
	return &thumbnails{root: root, store: store, decoding: make(chan struct{}, decoders), queue: make(chan struct{}, decoders+queued), pending: map[string]bool{}}, nil
}

func (t *thumbnails) close() error { return t.root.Close() }

// fileOf is the name of an artifact's thumbnail in the thumbnail dir.
func fileOf(id string) string { return id + ".webp" }

func (t *thumbnails) get(ctx context.Context, id string) ([]byte, error) {
	if !storage.IsID(id) {
		return nil, api.ArtifactNotFound(id)
	}
	thumb, err := t.root.ReadFile(fileOf(id))
	if err == nil {
		return thumb, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, api.StorageError("read thumbnail: " + err.Error())
	}
	// Requests for the same thumbnail share one making of it, which carries on if they give up: the
	// next one finds it cached.
	made := t.making.DoChan(id, func() (any, error) { return t.make(context.WithoutCancel(ctx), id) })
	select {
	case r := <-made:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.([]byte), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *thumbnails) make(ctx context.Context, id string) ([]byte, error) {
	select {
	case t.queue <- struct{}{}:
		defer func() { <-t.queue }()
	default:
		// Not cached either: the next request makes it afresh.
		return nil, api.TooManyAttempts(1)
	}
	t.decoding <- struct{}{}
	defer func() { <-t.decoding }()
	// Pending before the original is opened: a delete either comes first, and there's nothing to
	// open, or it marks this one. The singleflight makes one at a time per id.
	t.mu.Lock()
	t.pending[id] = false
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
	}()

	o, err := t.store.Read(ctx, id, "")
	if err != nil {
		return nil, storageError(id, err)
	}
	defer o.Body.Close()
	if !hasThumbnail(o.Artifact.ContentType) {
		return nil, fmt.Errorf("%w: %s has none", ErrNoThumbnail, o.Artifact.ContentType)
	}
	original := bytes.NewBuffer(make([]byte, 0, min(max(o.ContentLength, 0), api.MaxUploadBytes)+bytes.MinRead))
	if _, err := original.ReadFrom(o.Body); err != nil {
		return nil, api.StorageError("read artifact: " + err.Error())
	}
	thumb, err := shrink(original.Bytes())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoThumbnail, err)
	}
	deleted, err := t.cache(id, thumb)
	if err != nil {
		slog.Warn("Couldn't cache a thumbnail", "id", id, "err", err)
	}
	if deleted {
		return nil, api.ArtifactNotFound(id)
	}
	return thumb, nil
}

// cache writes a thumbnail aside, then renames it: a request racing this one never reads half a file.
// It tells whether the artifact was deleted meanwhile, in which case nothing is cached.
func (t *thumbnails) cache(id string, thumb []byte) (deleted bool, err error) {
	f, temp, err := storage.CreateTemp(t.root, id+".")
	if err != nil {
		return false, err
	}
	_, err = f.Write(thumb)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		t.mu.Lock()
		if deleted = t.pending[id]; !deleted {
			err = t.root.Rename(temp, fileOf(id))
		}
		t.mu.Unlock()
	}
	if err != nil || deleted {
		_ = t.root.Remove(temp)
	}
	return deleted, err
}

// remove deletes an artifact's thumbnail, and keeps one being made from being cached.
func (t *thumbnails) remove(id string) {
	if !storage.IsID(id) {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.pending[id]; ok {
		t.pending[id] = true
	}
	_ = t.root.Remove(fileOf(id))
}

// shrink decodes an image, fits it in the bounding box (never enlarging it), turns it upright and
// encodes it as WebP.
func shrink(original []byte) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(original))
	if err != nil {
		return nil, err
	}
	if size := decodedSize(format, cfg, original); size > decodeBudget {
		return nil, fmt.Errorf("decoding a %dx%d %s would take about %d MiB, over %d", cfg.Width, cfg.Height, format, size>>20, decodeBudget>>20)
	}
	img, _, err := image.Decode(bytes.NewReader(original))
	if err != nil {
		return nil, err
	}
	// The box is square, so fitting before turning gives the same size as after, for less work.
	small := orient(fit(img, thumbSize), orientationOf(format, original))
	var out bytes.Buffer
	if err := webp.Encode(&out, small, webp.Options{Quality: 80, Method: 4}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// decodedSize estimates the memory decoding an image takes at its peak, from its header. The factors
// are what each decoder was measured, or read in its source, to allocate per pixel, rounded up:
//   - 4 bytes for 8-bit images, 8 for 16-bit ones, whatever the decoder keeps (paletted and gray
//     ones take less, but count as 8-bit RGBA).
//   - An interlaced PNG decodes each pass into an image of its own before merging them: twice that.
//   - A progressive JPEG keeps every DCT coefficient as an int32 until its last scan: 16 bytes for
//     YCbCr or gray, 24 for CMYK. Baseline ones decode as they go: 4, 8 for CMYK.
//
// A header that doesn't read cleanly is priced as the costliest kind of its format: the decoders
// put up with more than these readers do (Go's JPEG one skips junk between segments), so an image
// must show it's the cheap kind to be counted as one.
//   - WebP decodes in wasm memory, which grows by copying: 8 for lossy, 26 for lossless. An
//     animation decodes every frame, at 4 bytes per pixel each, which the copies more than double:
//     16 per frame, plus 32 for its canvases.
func decodedSize(format string, cfg image.Config, original []byte) int64 {
	pixels := int64(cfg.Width) * int64(cfg.Height)
	perPixel := int64(4)
	switch cfg.ColorModel {
	case color.RGBA64Model, color.NRGBA64Model, color.Gray16Model:
		perPixel = 8
	}
	switch format {
	case "png":
		if pngInterlaced(original) {
			perPixel *= 2
		}
	case "jpeg":
		cmyk := cfg.ColorModel == color.CMYKModel
		switch {
		case !jpegBaseline(original) && cmyk:
			perPixel = 24
		case !jpegBaseline(original):
			perPixel = 16
		case cmyk:
			perPixel = 8
		}
	case "webp":
		lossy, frames := webpKind(original)
		switch {
		case frames > 0:
			perPixel = 16*int64(frames) + 32
		case lossy:
			perPixel = 8
		default:
			perPixel = 26
		}
	}
	return pixels * perPixel
}

// pngInterlaced reads the interlace method from a PNG's header, which DecodeConfig checked is there.
func pngInterlaced(data []byte) bool {
	const interlace = 8 + 8 + 12
	return len(data) > interlace && data[interlace] != 0
}

// jpegBaseline tells whether a JPEG's frame header, read before its first scan, is a baseline one.
// Anything else, a progressive frame or segments it can't follow, is not.
func jpegBaseline(data []byte) bool {
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return false
		}
		marker := data[i+1]
		if marker == 0xFF {
			i++
			continue
		}
		switch marker {
		case 0xC0, 0xC1:
			return true
		case 0xD8, 0xD9, 0xDA, 0x01, 0xD0, 0xD1, 0xD2, 0xD3, 0xD4, 0xD5, 0xD6, 0xD7:
			// No frame header before these, or no length to skip them by.
			return false
		}
		if marker >= 0xC2 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			return false
		}
		length := int(binary.BigEndian.Uint16(data[i+2:]))
		if length < 2 {
			return false
		}
		i += 2 + length
	}
	return false
}

// webpKind tells whether a WebP is lossy, and how many frames it has if it's an animation. Lossy
// means its chunks all read cleanly and its image is a VP8 one, not a VP8L.
func webpKind(data []byte) (lossy bool, frames int) {
	if len(data) < 12 {
		return false, 0
	}
	var vp8, vp8l bool
	for i := 12; i < len(data); {
		if i+8 > len(data) {
			return false, frames
		}
		length := int(binary.LittleEndian.Uint32(data[i+4:]))
		if length > len(data)-i-8 {
			return false, frames
		}
		switch string(data[i : i+4]) {
		case "VP8 ":
			vp8 = true
		case "VP8L":
			vp8l = true
		case "ANMF":
			frames++
		}
		i += 8 + length + length%2
	}
	return vp8 && !vp8l, frames
}

// fit scales img down to fit a box of size pixels, keeping its aspect ratio.
func fit(img image.Image, size int) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= size && h <= size {
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(dst, dst.Rect, img, b.Min, draw.Src)
		return dst
	}
	scale := min(float64(size)/float64(w), float64(size)/float64(h))
	dst := image.NewRGBA(image.Rect(0, 0, max(1, int(math.Round(float64(w)*scale))), max(1, int(math.Round(float64(h)*scale)))))
	draw.CatmullRom.Scale(dst, dst.Rect, img, b, draw.Src, nil)
	return dst
}
