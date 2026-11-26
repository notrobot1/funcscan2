package base

import (
	"path/filepath"
	"strings"
)

// Language is everything the pipeline needs to know about one programming
// language. Only Name, Extensions and Extract are required; the hooks let a
// language add the cross-file passes that are specific to it, each at the
// point of the pipeline where the shared passes expect it.
type Language struct {
	// Name identifies the language ("go"); raw calls carry it in Language.
	Name string
	// Family groups languages that can call each other (Kotlin and Java share
	// "jvm"). A call never binds to a definition of another family. Empty
	// means the language is its own family.
	Family string
	// Extensions are the file suffixes the language owns, with the dot (".go").
	Extensions []string
	// CaseInsensitive marks a language whose identifiers ignore case, so its
	// names may also be matched folded.
	CaseInsensitive bool
	// RequireImportEvidence marks a language with no implicit cross-file
	// scope: a call into another file counts only when the caller's file
	// imports the callee or its file.
	RequireImportEvidence bool

	// Extract reads one file.
	Extract func(path string) (*FileResult, error)

	// MergeDeclarations folds the copies of one declaration that several files
	// of the language emit. Runs first, while every file's nodes still carry
	// the ids the extractor minted.
	MergeDeclarations func(c *Corpus)
	// ResolveTypeReferences binds qualified type references using the
	// language's own import rules. Runs once ids are final, before stubs are
	// matched to definitions by bare name.
	ResolveTypeReferences func(c *Corpus)
	// SkipCall reports a raw call the shared call pass must leave alone (a
	// language builtin, say).
	SkipCall func(rc *RawCall) bool
	// FilterCallCandidates narrows the definitions a bare call may bind to.
	// exact reports that what is left is backed by evidence as strong as an
	// import of the symbol itself.
	FilterCallCandidates func(c *Corpus, rc *RawCall, candidates []string) (kept []string, exact bool)
	// AfterCalls runs once the shared call pass is done.
	AfterCalls func(c *Corpus)
	// Resolvers run last, in order: member calls and anything else that needs
	// the whole corpus and final ids.
	Resolvers []Resolver
}

// FamilyName is the language's interop family.
func (l *Language) FamilyName() string {
	if l.Family != "" {
		return l.Family
	}
	return l.Name
}

// Resolver is one cross-file, language-specific pass. It may add nodes and
// edges to the corpus; a panic inside it is contained and reported as a
// warning, so one broken pass never costs the rest of the graph.
type Resolver struct {
	Name string
	Run  func(c *Corpus)
}

// Corpus is the merged output of every extracted file, handed to the
// cross-file passes to rewrite in place.
type Corpus struct {
	// Root is the resolved scan root: ids and source files are made relative
	// to it.
	Root string
	// Paths are the extracted files; PerFile holds each one's own result at
	// the same index.
	Paths   []string
	PerFile []*FileResult

	Nodes    []*Node
	Edges    []*Edge
	RawCalls []*RawCall

	// LanguageOf maps a source file to its language, nil when no registered
	// language owns it. Set by the pipeline.
	LanguageOf func(sourceFile string) *Language

	memo map[string]any
}

// Family is the interop family of the file's language, "" when unknown.
func (c *Corpus) Family(sourceFile string) string {
	if sourceFile == "" || c.LanguageOf == nil {
		return ""
	}
	if lang := c.LanguageOf(sourceFile); lang != nil {
		return lang.FamilyName()
	}
	return ""
}

// Memo returns the value cached under key, building it on first use. A hook
// that is called once per raw call keeps its indexes here. The pipeline clears
// the cache whenever nodes or edges may have changed under it.
func (c *Corpus) Memo(key string, build func() any) any {
	if v, ok := c.memo[key]; ok {
		return v
	}
	if c.memo == nil {
		c.memo = map[string]any{}
	}
	v := build()
	c.memo[key] = v
	return v
}

// ResetMemo drops everything Memo cached.
func (c *Corpus) ResetMemo() { c.memo = nil }

// FileNodeIDs maps each source file to the id of its file node: the node
// whose label is the file's base name.
func (c *Corpus) FileNodeIDs() map[string]string {
	out := map[string]string{}
	for _, n := range c.Nodes {
		if n.SourceFile != "" && n.Label == filepath.Base(n.SourceFile) {
			if _, ok := out[n.SourceFile]; !ok {
				out[n.SourceFile] = n.ID
			}
		}
	}
	return out
}

// IsTypeLikeDefinition reports whether a node can be what a type reference
// names: code, not a namespace, and labelled with a bare name rather than a
// call (`Run()`), a member (`.Close()`) or a dotted path.
func IsTypeLikeDefinition(n *Node) bool {
	if n.Type == "namespace" {
		return false
	}
	label := strings.TrimSpace(n.Label)
	if label == "" || strings.HasSuffix(label, ")") || strings.HasPrefix(label, ".") {
		return false
	}
	if strings.Contains(label, ".") {
		return false
	}
	return n.FileType == "code"
}

// UnresolvedCallsKey is the Node.Metadata key under which member calls into a
// type this corpus never declares are parked.
const UnresolvedCallsKey = "unresolved_calls"

const maxParkedCallsPerNode = 64

// ParkUnresolvedMemberCall keeps a member call whose receiver type is declared
// nowhere in this corpus.
//
// A single build can only bind `obj.method()` when the receiver's type is
// declared in the same build, so a call into another repository would be
// dropped with the receiver type already in hand. Parking the pair on the
// caller node lets a later merge of several graphs finish the edge.
//
// The entry carries names only, never node ids: ids are rewritten by later
// passes and a stale id inside metadata would fail silently.
func ParkUnresolvedMemberCall(caller *Node, callee, receiverType, lang string, rc *RawCall) {
	if caller == nil || callee == "" || receiverType == "" {
		return
	}
	parked, _ := caller.Metadata[UnresolvedCallsKey].([]map[string]any)
	if caller.Metadata[UnresolvedCallsKey] != nil && parked == nil {
		return // some other producer owns the key in a shape we do not know
	}
	if len(parked) >= maxParkedCallsPerNode {
		return
	}
	for _, previous := range parked {
		if previous["callee"] == callee && previous["receiver_type"] == receiverType {
			return
		}
	}
	entry := map[string]any{"callee": callee, "receiver_type": receiverType, "lang": lang}
	if rc.SourceLocation != "" {
		entry["line"] = rc.SourceLocation
	}
	if rc.SourceFile != "" {
		entry["file"] = rc.SourceFile
	}
	if caller.Metadata == nil {
		caller.Metadata = map[string]any{}
	}
	caller.Metadata[UnresolvedCallsKey] = append(parked, entry)
}
