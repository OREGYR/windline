package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

// mkicon.go renders Windline's application icon to an .ico file, which build.ps1
// then turns into the .syso that the linker embeds in the executable.
//
// The icon is deliberately not shipped as binary art. It is drawn by exactly the
// same code the running app uses for its tray icon, drawLogoPrimitives, so the
// file in the repository and the pixels in the notification area cannot drift
// apart, and anyone can read how the icon is made.
//
// Every entry is stored as a PNG. That is legal for any size (the format's own
// rule for 256 px, extended here to all of them) and it means one encoder path
// instead of two, using the PNG encoder already in clipboard.go.

// icoSizes is the set Windows asks for: 16 in the notification area, 20/24/32
// in the shell, 48 on the desktop, 256 in large-tile views.
var icoSizes = []int{16, 20, 24, 32, 48, 64, 128, 256}

// runMakeIcon writes the icon and returns the process exit code.
func runMakeIcon(args []string) int {
	out := flagValue(args, "--out")
	if out == "" {
		out = filepath.Join("assets", "windline.ico")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkicon:", err)
		return 1
	}

	var images [][]byte
	for _, size := range icoSizes {
		png, err := renderIconPNG(size)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mkicon: size %d: %v\n", size, err)
			return 1
		}
		images = append(images, png)
	}

	if err := writeICO(out, icoSizes, images); err != nil {
		fmt.Fprintln(os.Stderr, "mkicon:", err)
		return 1
	}
	fmt.Printf("mkicon: wrote %s (%d sizes: %v)\n", out, len(icoSizes), icoSizes)
	return 0
}

// renderIconPNG draws the logo at one size and encodes it as PNG bytes.
func renderIconPNG(size int) ([]byte, error) {
	s := newSurface(size, size)
	defer s.free()
	s.clear()
	drawLogoPrimitives(s, size, false)
	s.flush()
	data, ok := encodePNGToMemory(s.bitmap)
	if !ok {
		return nil, fmt.Errorf("PNG encoding failed")
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("PNG encoding produced nothing")
	}
	return data, nil
}

// writeICO assembles the ICONDIR header, one ICONDIRENTRY per image, then the
// image payloads.
func writeICO(path string, sizes []int, images [][]byte) error {
	const (
		iconDirSize   = 6
		iconEntrySize = 16
	)
	var buf bytes.Buffer
	buf.Write([]byte{0, 0})                            // reserved
	binary.Write(&buf, binary.LittleEndian, uint16(1)) // type: 1 = icon
	binary.Write(&buf, binary.LittleEndian, uint16(len(images)))

	offset := iconDirSize + iconEntrySize*len(images)
	for i, size := range sizes {
		data := images[i]
		// A 256 px dimension is recorded as 0: the field is a single byte.
		dim := byte(size)
		if size >= 256 {
			dim = 0
		}
		buf.WriteByte(dim)                                  // width
		buf.WriteByte(dim)                                  // height
		buf.WriteByte(0)                                    // palette size
		buf.WriteByte(0)                                    // reserved
		binary.Write(&buf, binary.LittleEndian, uint16(1))  // colour planes
		binary.Write(&buf, binary.LittleEndian, uint16(32)) // bits per pixel
		binary.Write(&buf, binary.LittleEndian, uint32(len(data)))
		binary.Write(&buf, binary.LittleEndian, uint32(offset))
		offset += len(data)
	}
	for _, data := range images {
		buf.Write(data)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
