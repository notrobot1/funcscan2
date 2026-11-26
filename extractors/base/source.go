package base

import (
	"bytes"
	"encoding/binary"
	"os"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

var (
	utf32BOMs = [][]byte{{0xff, 0xfe, 0x00, 0x00}, {0x00, 0x00, 0xfe, 0xff}}
	utf16BOMs = [][]byte{{0xff, 0xfe}, {0xfe, 0xff}}
)

// ReadSourceBytes reads a source file as the UTF-8 bytes tree-sitter parses.
//
// tree-sitter treats its input as UTF-8. Valid UTF-8, with or without a BOM,
// is returned byte-for-byte. A UTF-16 or UTF-32 file with a BOM is decoded
// exactly and re-encoded as UTF-8. Anything else is decoded as Windows-1252,
// then Latin-1 (which cannot fail), so names come out whole; line numbers are
// unchanged in every case. The fallback is fixed, not the host locale:
// extraction must give the same graph on every machine.
func ReadSourceBytes(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if utf8.Valid(raw) {
		return raw, nil
	}
	for _, bom := range utf32BOMs {
		if bytes.HasPrefix(raw, bom) {
			if out, ok := decodeUTF32(raw[4:], bom[0] == 0xff); ok {
				return out, nil
			}
			// FF FE 00 00 is also UTF-16-LE: BOM + U+0000.
		}
	}
	for _, bom := range utf16BOMs {
		if bytes.HasPrefix(raw, bom) {
			if out, ok := decodeUTF16(raw[2:], bom[0] == 0xff); ok {
				return out, nil
			}
		}
	}
	return decodeSingleByte(raw), nil
}

func decodeUTF32(data []byte, littleEndian bool) ([]byte, bool) {
	if len(data)%4 != 0 {
		return nil, false
	}
	var order binary.ByteOrder = binary.BigEndian
	if littleEndian {
		order = binary.LittleEndian
	}
	out := make([]byte, 0, len(data)/2)
	for i := 0; i < len(data); i += 4 {
		r := rune(order.Uint32(data[i:]))
		if !utf8.ValidRune(r) {
			return nil, false
		}
		out = utf8.AppendRune(out, r)
	}
	return out, true
}

func decodeUTF16(data []byte, littleEndian bool) ([]byte, bool) {
	if len(data)%2 != 0 {
		return nil, false
	}
	var order binary.ByteOrder = binary.BigEndian
	if littleEndian {
		order = binary.LittleEndian
	}
	units := make([]uint16, len(data)/2)
	for i := range units {
		units[i] = order.Uint16(data[2*i:])
	}
	out := make([]byte, 0, len(data))
	for _, r := range utf16.Decode(units) {
		if r == unicode.ReplacementChar {
			return nil, false // unpaired surrogate
		}
		out = utf8.AppendRune(out, r)
	}
	return out, true
}

// decodeSingleByte decodes Windows-1252, or Latin-1 when the data holds a byte
// Windows-1252 leaves undefined.
func decodeSingleByte(raw []byte) []byte {
	latin1 := false
	for _, c := range raw {
		switch c {
		case 0x81, 0x8d, 0x8f, 0x90, 0x9d:
			latin1 = true
		}
	}
	out := make([]byte, 0, len(raw)+len(raw)/4)
	for _, c := range raw {
		r := rune(c)
		if !latin1 {
			r = charmap.Windows1252.DecodeByte(c)
		}
		out = utf8.AppendRune(out, r)
	}
	return out
}
