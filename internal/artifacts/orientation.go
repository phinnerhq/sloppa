package artifacts

import (
	"bytes"
	"encoding/binary"
	"image"
)

// Cameras and phones store photos as the sensor saw them, with an EXIF tag saying how to turn them
// upright. Browsers honour it, so a thumbnail must too, or it shows a portrait photo lying down.

// orientationOf is the EXIF orientation of an image of the format image.Decode named, from 1 (as
// stored) to 8; 1 when there's none or it can't be read.
func orientationOf(format string, data []byte) int {
	var exif []byte
	switch format {
	case "jpeg":
		exif = jpegExif(data)
	case "png":
		exif = pngExif(data)
	case "webp":
		exif = webpExif(data)
	}
	return tiffOrientation(exif)
}

var exifHeader = []byte("Exif\x00\x00")

// jpegExif is the payload of a JPEG's APP1 Exif segment, which comes before the image data.
func jpegExif(data []byte) []byte {
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil
	}
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return nil
		}
		marker := data[i+1]
		if marker == 0xFF {
			i++ // fill byte
			continue
		}
		// Start of scan, end of image: no metadata from here on.
		if marker == 0xDA || marker == 0xD9 {
			return nil
		}
		length := int(binary.BigEndian.Uint16(data[i+2:]))
		end := i + 2 + length
		if length < 2 || end > len(data) {
			return nil
		}
		if payload := data[i+4 : end]; marker == 0xE1 && bytes.HasPrefix(payload, exifHeader) {
			return payload[len(exifHeader):]
		}
		i = end
	}
	return nil
}

// pngExif is a PNG's eXIf chunk.
func pngExif(data []byte) []byte {
	const signature = "\x89PNG\r\n\x1a\n"
	if !bytes.HasPrefix(data, []byte(signature)) {
		return nil
	}
	for i := len(signature); i+8 <= len(data); {
		length := int(binary.BigEndian.Uint32(data[i:]))
		kind := string(data[i+4 : i+8])
		start, end := i+8, i+8+length
		if length < 0 || end > len(data) || kind == "IDAT" || kind == "IEND" {
			return nil
		}
		if kind == "eXIf" {
			return data[start:end]
		}
		i = end + 4 // CRC
	}
	return nil
}

// webpExif is a WebP's EXIF chunk, with or without the JPEG-style header some writers leave on it.
func webpExif(data []byte) []byte {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return nil
	}
	for i := 12; i+8 <= len(data); {
		kind := string(data[i : i+4])
		length := int(binary.LittleEndian.Uint32(data[i+4:]))
		start, end := i+8, i+8+length
		if length < 0 || end > len(data) {
			return nil
		}
		if kind == "EXIF" {
			return bytes.TrimPrefix(data[start:end], exifHeader)
		}
		i = end + length%2 // chunks are padded to an even size
	}
	return nil
}

// tiffOrientation reads the Orientation tag (0x0112) of a TIFF structure's first directory.
func tiffOrientation(data []byte) int {
	if len(data) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	if order.Uint16(data[2:]) != 42 {
		return 1
	}
	ifd := int(order.Uint32(data[4:]))
	if ifd < 8 || ifd+2 > len(data) {
		return 1
	}
	entries := int(order.Uint16(data[ifd:]))
	for i := range entries {
		entry := ifd + 2 + i*12
		if entry+12 > len(data) {
			return 1
		}
		// Type 3 is SHORT, stored in the entry itself.
		if order.Uint16(data[entry:]) == 0x0112 && order.Uint16(data[entry+2:]) == 3 {
			if o := int(order.Uint16(data[entry+8:])); o >= 1 && o <= 8 {
				return o
			}
			return 1
		}
	}
	return 1
}

// orient turns an image as stored into one upright, for EXIF orientation o.
func orient(src *image.RGBA, o int) *image.RGBA {
	if o <= 1 || o > 8 {
		return src
	}
	w, h := src.Rect.Dx(), src.Rect.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range dh {
		for x := range dw {
			// Where the upright pixel (x, y) is in the stored image.
			var sx, sy int
			switch o {
			case 2: // mirror
				sx, sy = w-1-x, y
			case 3: // turn 180°
				sx, sy = w-1-x, h-1-y
			case 4: // flip
				sx, sy = x, h-1-y
			case 5: // transpose
				sx, sy = y, x
			case 6: // turn 90° clockwise
				sx, sy = y, h-1-x
			case 7: // transverse
				sx, sy = w-1-y, h-1-x
			case 8: // turn 90° counterclockwise
				sx, sy = w-1-y, x
			}
			s := src.PixOffset(src.Rect.Min.X+sx, src.Rect.Min.Y+sy)
			copy(dst.Pix[dst.PixOffset(x, y):][:4], src.Pix[s:s+4])
		}
	}
	return dst
}
