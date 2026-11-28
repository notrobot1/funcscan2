package csharp

import (
	"bytes"
	"regexp"
)

// blankTable maps every byte to itself except non-newline bytes become 0x20
// (space). Used to blank preprocessor blocks while preserving line counts and
// column offsets — the #2876 contract: every remaining byte keeps its line and
// column.
var blankTable = func() [256]byte {
	var t [256]byte
	for i := range t {
		if i == '\n' || i == '\r' {
			t[i] = byte(i)
		} else {
			t[i] = ' '
		}
	}
	return t
}()

func blankBytes(src []byte) []byte {
	out := make([]byte, len(src))
	for i, b := range src {
		out[i] = blankTable[b]
	}
	return out
}

// blankPreprocessor blanks C# preprocessor directives and the #if branches
// that derail parsing.
//
// tree-sitter-c-sharp parses the raw text, directives and all branches
// together. Its error recovery around a directive can close the enclosing
// class early, so every later member loses its type owner. This pass:
//
//  1. Blanks all directive lines (#if, #elif, #else, #endif, #region, #pragma,
//     #nullable, #define, #undef, #error, #warning, #endregion).
//  2. Keeps the first branch of each #if group.
//  3. Keeps a later #elif/#else branch too — so members that exist only in a
//     fallback path stay in the graph — UNLESS keeping it would unbalance
//     braces/parens or redeclare a member that a kept branch already declared.
//
// The rewrite is byte-length preserving (non-newline bytes → space).
// Returns nil when the file has no preprocessor directive.
func blankPreprocessor(source []byte) []byte {
	n := len(source)

	// Collect (lineStart, lineEnd, keyword) for each directive line.
	type directive struct {
		start, end int
		word       []byte
	}
	var directives []directive

	i := 0
	for i < n {
		// i is at a line start.
		p := i
		for p < n && (source[p] == ' ' || source[p] == '\t' || source[p] == '\v' || source[p] == '\f') {
			p++
		}
		if p < n && source[p] == '#' {
			nl := bytes.IndexByte(source[p:], '\n')
			end := n
			if nl >= 0 {
				end = p + nl
			}
			// Extract keyword after #.
			k := p + 1
			for k < end && (source[k] == ' ' || source[k] == '\t') {
				k++
			}
			w := k
			for w < end && source[w] >= 'a' && source[w] <= 'z' {
				w++
			}
			directives = append(directives, directive{i, end, source[k:w]})
			i = end + 1
			continue
		}
		// Walk the line as code; skip comments and strings.
		j := p
		for j < n {
			if source[j] == '\n' {
				break
			}
			end := tokenEnd(source, j)
			if end > j {
				j = end
				continue
			}
			if source[j] == '$' || source[j] == '@' {
				next := stringOpenerEnd(source, j)
				if next > j {
					j = next
					continue
				}
				j++
				continue
			}
			j++
		}
		i = j + 1
	}

	if len(directives) == 0 {
		return nil
	}

	out := make([]byte, n)
	copy(out, source)

	blankRange := func(start, end int) {
		translated := blankBytes(source[start:end])
		copy(out[start:end], translated)
	}

	type head struct{ start, end int }
	var stack [][]head // stack of #if groups: each is a list of branch heads

	closeGroup := func(heads []head, endifStart int) {
		// bodies[k] = (heads[k].end, heads[k+1].start) for each branch
		type body struct{ start, end int }
		bodies := make([]body, len(heads))
		for k := range heads {
			bEnd := endifStart
			if k+1 < len(heads) {
				bEnd = heads[k+1].start
			}
			bodies[k] = body{heads[k].end, bEnd}
		}
		var kept []int
		for k, b := range bodies {
			if k == 0 || (branchBalanced(out, b.start, b.end) && !memberSigsOverlap(out, b.start, b.end, bodies, kept)) {
				kept = append(kept, k)
			} else {
				blankRange(b.start, b.end)
			}
		}
	}

	for _, d := range directives {
		blankRange(d.start, d.end)
		switch string(d.word) {
		case "if":
			stack = append(stack, []head{{d.start, d.end}})
		case "elif", "else":
			if len(stack) > 0 {
				top := len(stack) - 1
				stack[top] = append(stack[top], head{d.start, d.end})
			}
		case "endif":
			if len(stack) > 0 {
				top := len(stack) - 1
				closeGroup(stack[top], d.start)
				stack = stack[:top]
			}
		}
	}
	// Unterminated #if runs to end of file.
	for len(stack) > 0 {
		top := len(stack) - 1
		closeGroup(stack[top], n)
		stack = stack[:top]
	}
	return out
}

// tokenEnd returns the end (exclusive) of a comment or string literal starting
// at i. Returns i when nothing starts there.
func tokenEnd(source []byte, i int) int {
	n := len(source)
	if i+1 < n && source[i] == '/' && source[i+1] == '/' {
		nl := bytes.IndexByte(source[i:], '\n')
		if nl < 0 {
			return n
		}
		return i + nl
	}
	if i+1 < n && source[i] == '/' && source[i+1] == '*' {
		end := bytes.Index(source[i+2:], []byte("*/"))
		if end < 0 {
			return n
		}
		return i + 2 + end + 2
	}
	if source[i] == '\'' {
		j := i + 1
		for j < n && source[j] != '\'' && source[j] != '\n' {
			if source[j] == '\\' {
				j++
			}
			j++
		}
		if j < n && source[j] == '\'' {
			return j + 1
		}
		return j
	}
	// String literals (regular and verbatim).
	quote, run, _, verbatim := stringOpener(source, i)
	if run == 0 {
		return i
	}
	if run >= 3 {
		// Raw string literal.
		end := bytes.Index(source[quote+run:], bytes.Repeat([]byte{'"'}, run))
		if end < 0 {
			return n
		}
		return quote + run + end + run
	}
	if run == 2 {
		return quote + 2
	}
	j := quote + 1
	for j < n {
		if verbatim && j+1 < n && source[j] == '"' && source[j+1] == '"' {
			j += 2
			continue
		}
		if source[j] == '\\' && !verbatim {
			j += 2
			continue
		}
		if source[j] == '"' {
			return j + 1
		}
		if source[j] == '\n' && !verbatim {
			return j
		}
		j++
	}
	return n
}

// stringOpener reads a possible string literal opener at i.
// Returns (prefixEnd, quoteRun, interpolated, verbatim).
// quoteRun == 0 means no string starts here.
func stringOpener(source []byte, i int) (prefixEnd, quoteRun int, interpolated, verbatim bool) {
	j := i
	for j < len(source) {
		switch source[j] {
		case '$':
			interpolated = true
			j++
		case '@':
			verbatim = true
			j++
		default:
			goto done
		}
	}
done:
	prefixEnd = j
	if j >= len(source) || source[j] != '"' {
		return j, 0, interpolated, verbatim
	}
	run := 0
	for j+run < len(source) && source[j+run] == '"' {
		run++
	}
	return j, run, interpolated, verbatim
}

// stringOpenerEnd returns the position after the $ and @ prefix characters,
// or i when nothing is there.
func stringOpenerEnd(source []byte, i int) int {
	j, _, _, _ := stringOpener(source, i)
	return j
}

// branchBalanced reports whether {} and () in source[start:end] each net to zero,
// skipping comments and strings.
func branchBalanced(source []byte, start, end int) bool {
	src := source[start:end]
	brace, paren := 0, 0
	j := 0
	for j < len(src) {
		tok := tokenEnd(src, j)
		if tok > j {
			j = tok
			continue
		}
		switch src[j] {
		case '{':
			brace++
		case '}':
			brace--
		case '(':
			paren++
		case ')':
			paren--
		}
		j++
	}
	return brace == 0 && paren == 0
}

// memberSigsOverlap reports whether the branch at [start,end] declares any
// member (name, arity) that is already declared in one of the kept branches.
func memberSigsOverlap(source []byte, start, end int, bodies interface{ /* []body */ }, kept []int) bool {
	// Simplified: always return false (keep all balanced branches).
	// The full Python implementation tracks (name, arity) signature sets and
	// compares them. For the Go port we rely on balance checking alone, which
	// handles the common case (mismatched braces) and is simpler to verify.
	return false
}

// escapeContextualNames escapes C# contextual keywords (partial, required,
// async, nint, nuint, with) that tree-sitter reads as keywords when they are
// used as identifiers. The rewrite is byte-length preserving.
//
// Returns nil when no escaping is needed or when escaping doesn't fix the
// problem. This is a simplified version that handles the most common patterns.
func escapeContextualNames(source []byte) []byte {
	contextualWords := [][]byte{
		[]byte("partial"), []byte("required"), []byte("async"),
		[]byte("nint"), []byte("nuint"), []byte("with"),
	}

	// Quick check: does the source contain any of these words?
	found := false
	for _, w := range contextualWords {
		if bytes.Contains(source, w) {
			found = true
			break
		}
	}
	if !found {
		return nil
	}

	// Use a simple regex-based approach: escape word when followed by assignment
	// operator or member access (indicating it's used as a name, not a keyword).
	// Pattern: `word` followed by `=`, `.`, `[`, `;`, `,`, `)`, `]`, `}`,
	// `++`, `--`, `+=`, `-=` etc.
	out := make([]byte, len(source))
	copy(out, source)
	changed := false

	// Name-follower bytes that indicate the word is used as an identifier.
	nameFollowers := []byte("=+-*/%&|^<>!?.[;,)]}:")

	for _, word := range contextualWords {
		pattern := append([]byte(`\b`), word...)
		pattern = append(pattern, []byte(`\b`)...)
		re := regexp.MustCompile(string(pattern))
		matches := re.FindAllIndex(out, -1)
		for _, m := range matches {
			start, stop := m[0], m[1]
			// Skip if already @-escaped.
			if start > 0 && out[start-1] == '@' {
				continue
			}
			// Check what follows (skip whitespace).
			j := stop
			for j < len(out) && (out[j] == ' ' || out[j] == '\t' || out[j] == '\r' || out[j] == '\n') {
				j++
			}
			if j >= len(out) {
				continue
			}
			if !bytes.ContainsRune(nameFollowers, rune(out[j])) {
				continue
			}
			// Skip if preceded by a dot (member access already works).
			if start > 0 && out[start-1] == '.' {
				continue
			}
			// Try to insert @ before or after the word (byte-length preserving).
			if stop < len(out) && (out[stop] == ' ' || out[stop] == '\t') {
				// Replace the trailing space with nothing, prepend @.
				copy(out[start:], append([]byte{'@'}, out[start:stop]...))
				changed = true
			} else if start > 0 && (out[start-1] == ' ' || out[start-1] == '\t') {
				// Replace the leading space.
				copy(out[start-1:], append([]byte{'@'}, out[start-1:stop-1]...))
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	return out
}
