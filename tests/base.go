package extractors

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	sitter "github.com/tree-sitter/go-tree-sitter"
	"golang.org/x/text/cases"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/unicode/norm"
)

// LanguageBuiltinGlobals are language built-in globals that an AST may classify
// as call targets when used as constructors or coercion functions (String(x),
// Number(x), Boolean(x)). Without this filter they become god-nodes
// accumulating spurious edges from every call site. The set is shared by every
// language's extractor; Go's own predeclared functions are handled separately
// and only for bare identifiers (see goPredeclaredFuncs).
var LanguageBuiltinGlobals = map[string]struct{}(newSet(
	// JavaScript / TypeScript ECMAScript built-ins
	"String", "Number", "Boolean", "Object", "Array", "Symbol", "BigInt",
	"Date", "RegExp", "Error", "TypeError", "RangeError", "SyntaxError",
	"ReferenceError", "EvalError", "URIError",
	"Promise", "Map", "Set", "WeakMap", "WeakSet", "JSON", "Math",
	"Reflect", "Proxy", "Intl",
	"parseInt", "parseFloat", "isNaN", "isFinite",
	"encodeURIComponent", "decodeURIComponent", "encodeURI", "decodeURI",
	// Browser / Node common globals
	"URL", "URLSearchParams", "FormData", "Blob", "File",
	"Headers", "Request", "Response", "AbortController", "AbortSignal",
	"TextEncoder", "TextDecoder", "console",
	// Python built-in callables
	"str", "int", "float", "bool", "list", "dict", "set", "tuple", "bytes",
	"len", "range", "enumerate", "zip", "map", "filter", "sum", "min", "max",
	"print", "open", "isinstance", "type", "super", "sorted", "reversed",
	"any", "all", "abs", "round", "next", "iter", "hash", "id", "repr",
	"callable", "getattr", "setattr", "hasattr", "delattr", "vars", "dir",
	// Swift standard library / Foundation / SwiftUI
	"Int", "Int8", "Int16", "Int32", "Int64",
	"UInt", "UInt8", "UInt16", "UInt32", "UInt64",
	"Double", "Float", "Bool", "Character",
	"Sendable", "Codable", "Decodable", "Encodable", "Equatable", "Hashable",
	"Identifiable", "Comparable", "CaseIterable", "RawRepresentable",
	"CustomStringConvertible", "CustomDebugStringConvertible", "AnyObject",
	"LocalizedError",
	"Data", "UUID", "Decimal", "Calendar", "Locale", "TimeZone", "Bundle",
	"IndexPath", "IndexSet", "NotificationCenter", "UserDefaults",
	"FileManager", "URLSession", "URLRequest", "URLComponents",
	"JSONDecoder", "JSONEncoder", "DateFormatter", "NumberFormatter",
	"ISO8601DateFormatter",
	"NSObject", "NSString", "NSError", "NSLock", "NSAttributedString",
	"DispatchQueue", "DispatchGroup", "OperationQueue", "RunLoop",
	"View", "Color", "Font",
))

type stringSet map[string]struct{}

func newSet(items ...string) stringSet {
	s := make(stringSet, len(items))
	for _, it := range items {
		s[it] = struct{}{}
	}
	return s
}

func (s stringSet) has(v string) bool {
	_, ok := s[v]
	return ok
}

// normalizeID folds one id string to its canonical form: caseless, every run
// of non-word characters replaced by a single underscore, no underscores at the
// edges. Letters of any script survive, so CJK or Cyrillic names keep distinct
// ids.
//
// Case folding and NFKC do not commute (folding can expand a character into a
// base letter plus a combining mark, which NFKC may then recompose), so the two
// are iterated to a fixpoint before the non-word filter runs. That keeps the
// result idempotent and the same whether or not the caller already folded it.
func normalizeID(s string) string {
	cur := s
	if isASCII(s) {
		cur = strings.ToLower(s)
	} else {
		fold := cases.Fold()
		for i := 0; i < 6; i++ {
			next := norm.NFKC.String(fold.String(cur))
			if next == cur {
				break
			}
			cur = next
		}
	}
	var b strings.Builder
	pendingSep := false
	for _, r := range cur {
		if r != '_' && (unicode.IsLetter(r) || unicode.IsNumber(r)) {
			if pendingSep && b.Len() > 0 {
				b.WriteByte('_')
			}
			pendingSep = false
			b.WriteRune(r)
		} else {
			pendingSep = true
		}
	}
	return b.String()
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// makeID builds a canonical node id from one or more name parts: the parts are
// joined with `_` (stray `_`/`.` edges stripped from each) and normalised.
func makeID(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, strings.Trim(p, "_."))
		}
	}
	return normalizeID(strings.Join(kept, "_"))
}

// normalizePath tidies a path the way the ids expect it: `.` segments and
// doubled separators are dropped, `..` is kept as written (it is not resolved
// against the file system or collapsed lexically).
func normalizePath(path string) string {
	slashed := filepath.ToSlash(path)
	var parts []string
	for _, part := range strings.Split(slashed, "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	joined := strings.Join(parts, "/")
	if strings.HasPrefix(slashed, "/") {
		joined = "/" + joined
	}
	if joined == "" {
		joined = "."
	}
	return filepath.FromSlash(joined)
}

// fileStem is the node-id prefix for a file and its symbols: the whole
// (normalised) path, extension dropped, with forward slashes. Using every
// segment means same-named files in different directories get distinct ids. An
// absolute path is encoded whole; pass repo-relative paths to keep the on-disk
// location out of the ids.
func fileStem(path string) string {
	slashed := filepath.ToSlash(path)
	name := slashed[strings.LastIndex(slashed, "/")+1:]
	if name == "" || name == "." {
		return ""
	}
	// A leading dot (`.gitignore`) or a trailing one is not an extension.
	if dot := strings.LastIndex(name, "."); dot > 0 && dot < len(name)-1 {
		return slashed[:len(slashed)-(len(name)-dot)]
	}
	return slashed
}

// parentDirName is the name of the file's directory as written in the path,
// empty when the path names none.
func parentDirName(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2]
}

var (
	utf32BOMs = [][]byte{{0xff, 0xfe, 0x00, 0x00}, {0x00, 0x00, 0xfe, 0xff}}
	utf16BOMs = [][]byte{{0xff, 0xfe}, {0xfe, 0xff}}
)

// readSourceBytes reads a source file as the UTF-8 bytes tree-sitter parses.
//
// tree-sitter treats its input as UTF-8. Valid UTF-8, with or without a BOM,
// is returned byte-for-byte. A UTF-16 or UTF-32 file with a BOM is decoded
// exactly and re-encoded as UTF-8. Anything else is decoded as Windows-1252,
// then Latin-1 (which cannot fail), so names come out whole; line numbers are
// unchanged in every case. The fallback is fixed, not the host locale:
// extraction must give the same graph on every machine.
func readSourceBytes(path string) ([]byte, error) {
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

func readText(n *sitter.Node, source []byte) string {
	if n == nil {
		return ""
	}
	return string(source[n.StartByte():n.EndByte()])
}

func children(n *sitter.Node) []*sitter.Node {
	count := n.ChildCount()
	out := make([]*sitter.Node, 0, count)
	for i := uint(0); i < count; i++ {
		if c := n.Child(i); c != nil {
			out = append(out, c)
		}
	}
	return out
}

func namedChildren(n *sitter.Node) []*sitter.Node {
	var out []*sitter.Node
	for _, c := range children(n) {
		if c.IsNamed() {
			out = append(out, c)
		}
	}
	return out
}

func line(n *sitter.Node) int {
	return int(n.StartPosition().Row) + 1
}

var goModuleLine = regexp.MustCompile(`(?m)^\s*module\s+(\S+)`)

// goImportPathForFile returns the canonical Go import path of the package a
// source file belongs to: the `module` path of the nearest go.mod plus the
// directory's path below it. Empty when the file is not inside a module.
func goImportPathForFile(absPath string) string {
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = resolved
	}
	pkgDir := filepath.Dir(absPath)
	for dir := pkgDir; ; dir = filepath.Dir(dir) {
		goMod := filepath.Join(dir, "go.mod")
		if info, err := os.Stat(goMod); err == nil && info.Mode().IsRegular() {
			// The nearest go.mod settles the module, even when it is unreadable.
			data, _ := os.ReadFile(goMod)
			match := goModuleLine.FindSubmatch(data)
			if match == nil {
				return ""
			}
			module := string(match[1])
			rel, err := filepath.Rel(dir, pkgDir)
			if err != nil {
				return ""
			}
			if rel == "." {
				return module
			}
			return module + "/" + filepath.ToSlash(rel)
		}
		if dir == filepath.Dir(dir) {
			return ""
		}
	}
}
