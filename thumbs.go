package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// thumbs.go turns a file on disk into the photo that hangs on the line.
//
// A thumbnail is decoded once, scaled down to roughly twice the size it is ever
// drawn at, and kept in memory: the line holds one per photo. The macOS original
// does the same with CGImageSource. Here the decoder is GDI+, which covers PNG,
// JPEG, BMP, GIF, TIFF and ICO. Windows' own HEIF and WebP codecs are picked up
// automatically when the extensions are installed.

// imageExtensions are accepted as images, matching the original's list.
var imageExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".heic": true,
	".tif": true, ".tiff": true, ".gif": true, ".webp": true,
	".bmp": true, ".ico": true,
}

// recordingExtensions are screen recordings, which hang with a play button.
var recordingExtensions = map[string]bool{
	".mov": true, ".mp4": true,
}

func isRecording(path string) bool {
	return recordingExtensions[strings.ToLower(filepath.Ext(path))]
}

func isImagePath(path string) bool {
	return imageExtensions[strings.ToLower(filepath.Ext(path))]
}

// thumb is one decoded photo, already scaled down. It owns the surface its
// pixels live in, so it must be freed when the item leaves the line.
//
// The pixels deliberately live in a surface rather than in a GDI+ bitmap made by
// cloning: GdipCloneBitmapArea over a scan0-backed bitmap does not produce an
// independent copy, so such a clone points at memory the surface frees. A surface
// owns its DIB, its bitmap wrapper and its graphics context together, and drawing
// one surface onto another is exactly what the renderer needs.
type thumb struct {
	surf    *surface
	W, H    int
	Version int
}

// thumbVersion bumps on every reload so the card cache knows to redraw.
var thumbVersion atomic.Int64

func (t *thumb) free() {
	if t != nil && t.surf != nil {
		t.surf.free()
		t.surf = nil
	}
}

// makeThumbnail decodes path and scales it so its longest side is maxPixels,
// never enlarging. It returns nil when the file is not a decodable image, which
// is how the original treats unknown formats.
func makeThumbnail(path string, maxPixels int) *thumb {
	full, ok := loadImageFile(path)
	if !ok {
		return nil
	}
	defer full.free()
	return thumbnailOf(full, path, maxPixels)
}

// thumbnailOf scales an already-decoded image, honouring the JPEG's EXIF
// orientation first: the original asks CGImageSource for a transformed thumbnail,
// so a photo taken sideways hangs upright here too.
func thumbnailOf(full *gpImage, path string, maxPixels int) *thumb {
	if full == nil || full.W <= 0 || full.H <= 0 {
		return nil
	}
	if strings.EqualFold(filepath.Ext(path), ".jpg") || strings.EqualFold(filepath.Ext(path), ".jpeg") {
		if rf, ok := exifRotateFlip(path); ok {
			call(gpImageRotateFlip, full.img, uintptr(rf))
			full.W, full.H = full.H, full.W
		}
	}
	if maxPixels < 16 {
		maxPixels = 16
	}
	w, h := full.W, full.H
	scale := 1.0
	if longest := max(w, h); longest > maxPixels {
		scale = float64(maxPixels) / float64(longest)
	}
	tw := max(1, int(float64(w)*scale+0.5))
	th := max(1, int(float64(h)*scale+0.5))

	s := newSurface(tw, th)
	s.clear()
	s.drawScaled(full, 0, 0, tw, th)
	s.flush()
	return &thumb{surf: s, W: tw, H: th, Version: int(thumbVersion.Add(1))}
}

// exifRotateFlip reads the EXIF orientation of a JPEG and maps it onto GDI+'s
// RotateFlipType, so a photo whose pixels are stored sideways hangs upright.
func exifRotateFlip(path string) (int, bool) {
	data, err := readHead(path, 128*1024)
	if err != nil || len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 0, false
	}
	// Walk the JPEG segments looking for APP1/Exif.
	for i := 2; i+4 < len(data); {
		if data[i] != 0xFF {
			i++
			continue
		}
		marker := data[i+1]
		if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		if i+4 > len(data) {
			return 0, false
		}
		size := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if size < 2 || i+2+size > len(data) {
			return 0, false
		}
		seg := data[i+4 : i+2+size]
		if marker == 0xE1 && len(seg) > 6 && string(seg[:4]) == "Exif" {
			if o, ok := tiffOrientation(seg[6:]); ok {
				return orientationToRotateFlip(o)
			}
		}
		i += 2 + size
	}
	return 0, false
}

// tiffOrientation reads tag 0x0112 out of the EXIF TIFF block.
func tiffOrientation(tiff []byte) (int, bool) {
	if len(tiff) < 8 {
		return 0, false
	}
	var bo binary.ByteOrder
	switch {
	case tiff[0] == 'I' && tiff[1] == 'I':
		bo = binary.LittleEndian
	case tiff[0] == 'M' && tiff[1] == 'M':
		bo = binary.BigEndian
	default:
		return 0, false
	}
	if bo.Uint16(tiff[2:4]) != 42 {
		return 0, false
	}
	ifdOff := int(bo.Uint32(tiff[4:8]))
	if ifdOff+2 > len(tiff) {
		return 0, false
	}
	count := int(bo.Uint16(tiff[ifdOff : ifdOff+2]))
	entry := ifdOff + 2
	for i := 0; i < count; i++ {
		if entry+12 > len(tiff) {
			return 0, false
		}
		tag := bo.Uint16(tiff[entry : entry+2])
		if tag == 0x0112 {
			if bo.Uint16(tiff[entry+2:entry+4]) != 3 { // SHORT
				return 0, false
			}
			return int(bo.Uint16(tiff[entry+8 : entry+10])), true
		}
		entry += 12
	}
	return 0, false
}

// orientationToRotateFlip maps an EXIF orientation (1..8) onto RotateFlipType.
func orientationToRotateFlip(o int) (int, bool) {
	switch o {
	case 1:
		return 0, false // already upright: no work to do
	case 2:
		return 4, true // RotateNoneFlipX
	case 3:
		return 2, true // Rotate180FlipNone
	case 4:
		return 6, true // Rotate180FlipX
	case 5:
		return 5, true // Rotate90FlipX
	case 6:
		return 1, true // Rotate90FlipNone
	case 7:
		return 7, true // Rotate270FlipX
	case 8:
		return 3, true // Rotate270FlipNone
	}
	return 0, false
}

// readHead reads at most n bytes from the start of a file. EXIF lives near the
// front, so reading the whole file would be wasteful for a 40 MB screenshot.
func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	read, err := f.Read(buf)
	if read <= 0 {
		return nil, err
	}
	return buf[:read], nil
}
