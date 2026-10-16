// Command mkrsrc compiles Windline's Windows resources into the COFF object the
// Go linker embeds as the executable's .rsrc section.
//
// It exists because the usual tools are either unavailable (windres needs a
// MinGW toolchain, which this project deliberately does not require) or
// incomplete (akavel/rsrc embeds an icon and a manifest but cannot write a
// VERSIONINFO block, so the .exe would show no name or version in Explorer).
// Writing the object directly keeps the build self-contained: `go build` plus
// this program, nothing else.
//
// The Go linker looks for a COFF section named ".rsrc" in any object file in the
// package directory and copies its contents into the final image as the resource
// directory, patching each IMAGE_REL_AMD64_ADDR32NB relocation with the section's
// virtual address plus the addend stored in the section data. That is exactly
// how the OffsetsToData fields below are filled in: each holds its offset from
// the start of the resource section, and a relocation turns it into the RVA the
// resource loader expects.
//
// Usage:
//
//	mkrsrc -ico assets/windline.ico -manifest windline.manifest -o windline_amd64.syso
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"sort"
	"unicode/utf16"
)

// ----------------------------------------------------------------- PE / COFF

const (
	machineAMD64     = 0x8664
	scnCntInitData   = 0x00000040
	scnMemRead       = 0x40000000
	symClassStatic   = 3
	relAmd64Addr32NB = 0x0003
)

// Resource types from winuser.h.
const (
	rtIcon      = 3
	rtGroupIcon = 14
	rtVersion   = 16
	rtManifest  = 24
)

// appVersion is stamped into the version resource; keep it in step with
// appVersion in main.go, which is what --version prints.
const appVersion = "1.0.0"

func main() {
	icoPath := flag.String("ico", "", "icon to embed (required)")
	manPath := flag.String("manifest", "", "application manifest to embed")
	out := flag.String("o", "windline_amd64.syso", "output object file")
	flag.Parse()

	if *icoPath == "" {
		fmt.Fprintln(os.Stderr, "mkrsrc: -ico is required")
		os.Exit(2)
	}

	section, err := buildSection(*icoPath, *manPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkrsrc:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, writeCOFF(section.data, section.relocs), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "mkrsrc:", err)
		os.Exit(1)
	}
	fmt.Printf("mkrsrc: wrote %s (resources: %d bytes, %d relocations)\n",
		*out, len(section.data), len(section.relocs))
}

// ----------------------------------------------------------------- resources

// resource is one leaf: a type, an id, and the bytes.
type resource struct {
	typ  uint32
	id   uint32
	data []byte
}

// section is the assembled .rsrc image plus the fields needing an RVA.
type section struct {
	data   []byte
	relocs []reloc
}

type reloc struct {
	off uint32 // offset of a 4-byte RVA field within the section
}

// buildSection assembles the whole resource directory: the icons as RT_ICON, the
// group descriptor that ties them together as RT_GROUP_ICON, the manifest, and
// the version block.
func buildSection(icoPath, manPath string) (section, error) {
	images, err := readICO(icoPath)
	if err != nil {
		return section{}, err
	}

	var res []resource
	// Each icon image gets its own RT_ICON entry, numbered from 1. Windows picks
	// the closest match to the requested size out of the group.
	group := make([]byte, 6)
	binary.LittleEndian.PutUint16(group[2:], uint16(1)) // type: icon
	binary.LittleEndian.PutUint16(group[4:], uint16(len(images)))
	for i, im := range images {
		id := uint32(i + 1)
		res = append(res, resource{typ: rtIcon, id: id, data: im.data})

		// GRPICONDIRENTRY is the ICO directory entry with a 2-byte id in place of
		// the 4-byte file offset.
		dim := byte(im.width)
		if im.width >= 256 {
			dim = 0
		}
		hdim := byte(im.height)
		if im.height >= 256 {
			hdim = 0
		}
		e := make([]byte, 14)
		e[0] = dim
		e[1] = hdim
		e[2] = im.colorCount
		e[3] = 0
		binary.LittleEndian.PutUint16(e[4:], 1) // planes
		binary.LittleEndian.PutUint16(e[6:], uint16(im.bitCount))
		binary.LittleEndian.PutUint32(e[8:], uint32(len(im.data)))
		binary.LittleEndian.PutUint16(e[12:], uint16(id))
		group = append(group, e...)
	}
	res = append(res, resource{typ: rtGroupIcon, id: 1, data: group})

	res = append(res, resource{typ: rtVersion, id: 1, data: buildVersionInfo(appVersion, appVersion, "Windline",
		"Windline - photos on a clothesline above your screen",
		"MIT licensed. A Windows port of Tendedero by Alejandro Bujan.")})

	if manPath != "" {
		man, err := os.ReadFile(manPath)
		if err != nil {
			return section{}, fmt.Errorf("reading manifest: %w", err)
		}
		res = append(res, resource{typ: rtManifest, id: 1, data: man})
	}

	return assemble(res), nil
}

// assemble lays out the resource directory tree and its payloads.
//
// The tree is three levels deep, which is what the resource loader expects:
//
//	level 1  type        3 = RT_ICON, 14 = RT_GROUP_ICON, 16 = RT_VERSION, ...
//	level 2  name/id     the resource's own id
//	level 3  language    1033 (US English), the language of the payload
//	then     data entry  where the payload lives and how big it is
//
// Skipping the language level does not produce an error anywhere: the directory
// simply parses, and then nothing reads it, because the loader looks up
// type/name/language and finds no match at the last step. That is why the icon
// worked (the shell falls back to the file itself) while the version block was
// silently ignored.
func assemble(res []resource) section {
	// Group by type, ids in ascending order, which is what the loader expects.
	byType := map[uint32][]resource{}
	var types []uint32
	for _, r := range res {
		if _, seen := byType[r.typ]; !seen {
			types = append(types, r.typ)
		}
		byType[r.typ] = append(byType[r.typ], r)
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	for _, t := range types {
		rs := byType[t]
		sort.Slice(rs, func(i, j int) bool { return rs[i].id < rs[j].id })
		byType[t] = rs
	}

	const (
		dirSize  = 16
		entSize  = 8
		dataSize = 16
	)
	const langID = 1033 // en-US

	// Pass one: work out where everything goes.
	rootOff := 0
	rootEntOff := rootOff + dirSize
	off := rootEntOff + entSize*len(types)

	typeDir := map[uint32]int{}
	typeEnt := map[uint32]int{}
	for _, t := range types {
		typeDir[t] = off
		off += dirSize
		typeEnt[t] = off
		off += entSize * len(byType[t])
	}

	// The language level: one directory per resource, each with a single entry.
	type key struct{ typ, id uint32 }
	langDir := map[key]int{}
	for _, t := range types {
		for _, r := range byType[t] {
			langDir[key{t, r.id}] = off
			off += dirSize + entSize
		}
	}

	// Data entries are a flat table; remember each one's offset.
	dataEnt := map[key]int{}
	for _, t := range types {
		for _, r := range byType[t] {
			dataEnt[key{t, r.id}] = off
			off += dataSize
		}
	}

	// Payloads, each 4-byte aligned.
	payload := map[key]int{}
	for _, t := range types {
		for _, r := range byType[t] {
			off = align4(off)
			payload[key{t, r.id}] = off
			off += len(r.data)
		}
	}
	off = align4(off)

	buf := make([]byte, off)
	var relocs []reloc

	put16 := func(at, v int) { binary.LittleEndian.PutUint16(buf[at:], uint16(v)) }
	put32 := func(at int, v uint32) { binary.LittleEndian.PutUint32(buf[at:], v) }

	// Level 1: the root directory, one entry per type, each pointing at that
	// type's directory. The high bit of OffsetToData marks a directory.
	put16(rootOff+14, len(types)) // NumberOfIdEntries
	for i, t := range types {
		at := rootEntOff + i*entSize
		put32(at, t)
		put32(at+4, uint32(typeDir[t])|0x80000000)
	}

	// Level 2: per-type directory, one entry per id, pointing at the language
	// directory for that resource.
	for _, t := range types {
		rs := byType[t]
		put16(typeDir[t]+14, len(rs))
		for i, r := range rs {
			k := key{t, r.id}
			at := typeEnt[t] + i*entSize
			put32(at, r.id)
			put32(at+4, uint32(langDir[k])|0x80000000)
		}
	}

	// Level 3: per-resource language directory, one entry, pointing at the data
	// entry. Offsets to data entries do NOT carry the high bit.
	for _, t := range types {
		for _, r := range byType[t] {
			k := key{t, r.id}
			d := langDir[k]
			put16(d+14, 1) // one language
			put32(d+dirSize, langID)
			put32(d+dirSize+4, uint32(dataEnt[k]))
		}
	}

	// Data entries. OffsetToData is the only RVA in the tree, so it is the only
	// field that needs a relocation; the addend it carries is the payload's
	// offset within the section, which the linker adds the section's base to.
	//
	// CodePage is 1200 (UTF-16) only for the version block, which really is
	// Unicode text; the icons and the manifest are raw bytes and say so with 0.
	for _, t := range types {
		for _, r := range byType[t] {
			k := key{t, r.id}
			at := dataEnt[k]
			put32(at, uint32(payload[k]))
			relocs = append(relocs, reloc{off: uint32(at)})
			put32(at+4, uint32(len(r.data))) // Size
			if t == rtVersion {
				put32(at+8, 1200) // CodePage: Unicode
			} else {
				put32(at+8, 0)
			}
			copy(buf[payload[k]:], r.data)
		}
	}

	return section{data: buf, relocs: relocs}
}

func align4(n int) int { return (n + 3) &^ 3 }

// ---------------------------------------------------------------------- ICO

type icoImage struct {
	width, height int
	colorCount    byte
	bitCount      int
	data          []byte
}

// readICO parses an .ico into its individual images, keeping the original
// payload bytes so a PNG entry stays a PNG.
func readICO(path string) ([]icoImage, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading icon: %w", err)
	}
	if len(raw) < 6 {
		return nil, fmt.Errorf("%s is too short to be an icon", path)
	}
	if binary.LittleEndian.Uint16(raw[0:]) != 0 || binary.LittleEndian.Uint16(raw[2:]) != 1 {
		return nil, fmt.Errorf("%s is not an icon file", path)
	}
	count := int(binary.LittleEndian.Uint16(raw[4:]))
	if count == 0 {
		return nil, fmt.Errorf("%s contains no images", path)
	}
	if len(raw) < 6+16*count {
		return nil, fmt.Errorf("%s is truncated", path)
	}

	images := make([]icoImage, 0, count)
	for i := 0; i < count; i++ {
		e := raw[6+16*i:]
		w, h := int(e[0]), int(e[1])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		size := int(binary.LittleEndian.Uint32(e[8:]))
		off := int(binary.LittleEndian.Uint32(e[12:]))
		if off+size > len(raw) {
			return nil, fmt.Errorf("%s: image %d runs past the end of the file", path, i)
		}
		images = append(images, icoImage{
			width:      w,
			height:     h,
			colorCount: e[2],
			bitCount:   int(binary.LittleEndian.Uint16(e[6:])),
			data:       raw[off : off+size],
		})
	}
	return images, nil
}

// -------------------------------------------------------------- VERSIONINFO

// buildVersionInfo assembles the RT_VERSION payload.
//
// The resource is a tree of self-describing blocks: each one starts with three
// WORDs (length in bytes, length of its value, and the value's type) followed by
// a NUL-terminated UTF-16 key, 4-byte padding, the value, more padding, and then
// its children. Lengths are filled in as each block is closed, because a parent's
// length includes all of its children.
func buildVersionInfo(fileVersion, productVersion, productName, description, copyright string) []byte {
	fv := parseVersion(fileVersion)

	fixed := make([]byte, 52)
	binary.LittleEndian.PutUint32(fixed[0:], 0xFEEF04BD)       // dwSignature
	binary.LittleEndian.PutUint32(fixed[4:], 0x00010000)       // dwStrucVersion
	binary.LittleEndian.PutUint32(fixed[8:], fv[0]<<16|fv[1])  // dwFileVersionMS
	binary.LittleEndian.PutUint32(fixed[12:], fv[2]<<16|fv[3]) // dwFileVersionLS
	binary.LittleEndian.PutUint32(fixed[16:], fv[0]<<16|fv[1]) // dwProductVersionMS
	binary.LittleEndian.PutUint32(fixed[20:], fv[2]<<16|fv[3]) // dwProductVersionLS
	binary.LittleEndian.PutUint32(fixed[24:], 0x3F)            // dwFileFlagsMask
	binary.LittleEndian.PutUint32(fixed[28:], 0)               // dwFileFlags
	binary.LittleEndian.PutUint32(fixed[32:], 0x40004)         // dwFileOS: VOS_NT_WINDOWS32
	binary.LittleEndian.PutUint32(fixed[36:], 0x1)             // dwFileType: VFT_APP
	binary.LittleEndian.PutUint32(fixed[40:], 0)               // dwFileSubtype
	binary.LittleEndian.PutUint32(fixed[44:], 0)               // dwFileDateMS
	binary.LittleEndian.PutUint32(fixed[48:], 0)               // dwFileDateLS

	strings := []struct{ key, val string }{
		{"CompanyName", productName},
		{"FileDescription", description},
		{"FileVersion", fileVersion},
		{"InternalName", "windline"},
		{"LegalCopyright", copyright},
		{"OriginalFilename", "windline.exe"},
		{"ProductName", productName},
		{"ProductVersion", productVersion},
	}
	var entries [][]byte
	for _, s := range strings {
		entries = append(entries, verNode(s.key, 1, len(utf16.Encode([]rune(s.val)))+1, wideZ(s.val), nil...))
	}
	stringTable := verNode("040904b0", 1, 0, nil, entries...)
	stringFileInfo := verNode("StringFileInfo", 1, 0, nil, stringTable)

	// One translation pair: US English, Unicode (1200).
	translation := make([]byte, 4)
	binary.LittleEndian.PutUint16(translation[0:], 0x0409)
	binary.LittleEndian.PutUint16(translation[2:], 1200)
	varFile := verNode("Translation", 0, 4, translation, nil...)
	varFileInfo := verNode("VarFileInfo", 1, 0, nil, varFile)

	return verNode("VS_VERSION_INFO", 0, len(fixed), fixed, stringFileInfo, varFileInfo)
}

// verNode builds one block: header, key, padding, optional value, children.
//
// valueLen is the length of the value in the units the format uses — bytes for
// binary values such as VS_FIXEDFILEINFO, UTF-16 code units for strings.
func verNode(key string, valueType, valueLen int, value []byte, children ...[]byte) []byte {
	var b bytes.Buffer
	b.Write(make([]byte, 6)) // header, filled in below
	b.Write(wideZ(key))
	padTo4(&b)
	if value != nil {
		b.Write(value)
		padTo4(&b)
	}
	for _, c := range children {
		b.Write(c)
	}
	out := b.Bytes()
	binary.LittleEndian.PutUint16(out[0:], uint16(len(out)))  // wLength
	binary.LittleEndian.PutUint16(out[2:], uint16(valueLen))  // wValueLength
	binary.LittleEndian.PutUint16(out[4:], uint16(valueType)) // wType
	return out
}

// wideZ encodes a string as NUL-terminated UTF-16.
func wideZ(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, (len(u)+1)*2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(out[i*2:], c)
	}
	return out
}

func padTo4(b *bytes.Buffer) {
	for b.Len()%4 != 0 {
		b.WriteByte(0)
	}
}

// parseVersion turns "1.2.3.4" into four numbers, ignoring anything that is not
// one. A version resource is four numbers wide, so a short string is padded.
func parseVersion(s string) [4]uint32 {
	var out [4]uint32
	n := 0
	cur := uint32(0)
	seen := false
	for _, r := range s {
		if r >= '0' && r <= '9' {
			cur = cur*10 + uint32(r-'0')
			seen = true
			continue
		}
		if seen && n < 4 {
			out[n] = cur
			n++
			cur, seen = 0, false
		}
	}
	if seen && n < 4 {
		out[n] = cur
	}
	return out
}

// ---------------------------------------------------------------------- COFF

// writeCOFF wraps the section in a minimal object file: a header, one section
// header, the section data, its relocations, and the single section symbol the
// relocations point at.
//
// The section symbol is what makes this work. A relocation must name a symbol,
// and every directory-entry offset in the resource tree is relative to the start
// of the section, so each one points at this symbol with the offset as its
// addend. The linker then writes "section base + addend", which is the RVA.
func writeCOFF(data []byte, relocs []reloc) []byte {
	const (
		fileHeaderSize = 20
		sectHeaderSize = 40
		symSize        = 18
		relSize        = 10
	)

	padded := append([]byte{}, data...)
	for len(padded)%4 != 0 {
		padded = append(padded, 0)
	}

	dataOff := fileHeaderSize + sectHeaderSize
	relOff := align4(dataOff + len(padded))
	symOff := relOff + relSize*len(relocs)

	var buf bytes.Buffer
	// IMAGE_FILE_HEADER
	binary.Write(&buf, binary.LittleEndian, uint16(machineAMD64))
	binary.Write(&buf, binary.LittleEndian, uint16(1)) // NumberOfSections
	binary.Write(&buf, binary.LittleEndian, uint32(0)) // TimeDateStamp
	binary.Write(&buf, binary.LittleEndian, uint32(symOff))
	binary.Write(&buf, binary.LittleEndian, uint32(1)) // NumberOfSymbols
	binary.Write(&buf, binary.LittleEndian, uint16(0)) // SizeOfOptionalHeader
	binary.Write(&buf, binary.LittleEndian, uint16(0)) // Characteristics

	// IMAGE_SECTION_HEADER
	name := make([]byte, 8)
	copy(name, ".rsrc")
	buf.Write(name)
	binary.Write(&buf, binary.LittleEndian, uint32(len(padded))) // VirtualSize
	binary.Write(&buf, binary.LittleEndian, uint32(0))           // VirtualAddress
	binary.Write(&buf, binary.LittleEndian, uint32(len(padded))) // SizeOfRawData
	binary.Write(&buf, binary.LittleEndian, uint32(dataOff))     // PointerToRawData
	binary.Write(&buf, binary.LittleEndian, uint32(relOff))      // PointerToRelocations
	binary.Write(&buf, binary.LittleEndian, uint32(0))           // PointerToLinenumbers
	binary.Write(&buf, binary.LittleEndian, uint16(len(relocs))) // NumberOfRelocations
	binary.Write(&buf, binary.LittleEndian, uint16(0))           // NumberOfLinenumbers
	binary.Write(&buf, binary.LittleEndian, uint32(scnCntInitData|scnMemRead))

	buf.Write(padded)

	// Relocations: 4-byte RVAs that the linker must fill in.
	for buf.Len() < relOff {
		buf.WriteByte(0)
	}
	for _, r := range relocs {
		binary.Write(&buf, binary.LittleEndian, r.off)     // VirtualAddress
		binary.Write(&buf, binary.LittleEndian, uint32(0)) // SymbolTableIndex: the section symbol
		binary.Write(&buf, binary.LittleEndian, uint16(relAmd64Addr32NB))
	}

	// Symbol table: the section symbol. Its name is inline because ".rsrc" fits.
	buf.Write(name)
	binary.Write(&buf, binary.LittleEndian, uint32(0)) // Value
	binary.Write(&buf, binary.LittleEndian, int16(1))  // SectionNumber
	binary.Write(&buf, binary.LittleEndian, uint16(0)) // Type: not a function
	buf.WriteByte(symClassStatic)                      // StorageClass
	buf.WriteByte(0)                                   // NumberOfAuxSymbols

	// String table: empty, but the size field is mandatory.
	binary.Write(&buf, binary.LittleEndian, uint32(4))

	return buf.Bytes()
}
