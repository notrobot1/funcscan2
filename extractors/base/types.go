// Package base holds what every language extractor and every cross-file pass
// shares: the graph types, node-id normalisation, source decoding, and the
// Language/Corpus contract a language plugs into the pipeline through.
package base

import "encoding/json"

// OptString is a string that serialises to JSON null when empty. It stands in
// for the `str | None` values of the original Python output.
type OptString string

func (s OptString) MarshalJSON() ([]byte, error) {
	if s == "" {
		return []byte("null"), nil
	}
	return json.Marshal(string(s))
}

func (s *OptString) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*s = ""
		return nil
	}
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*s = OptString(v)
	return nil
}

// TypeTable maps a binding name to its receiver type. A key that is present
// with an empty value is a binding whose type is unknown (Python's None): it
// still shadows package variables and imports of the same name.
type TypeTable map[string]string

func (t TypeTable) MarshalJSON() ([]byte, error) {
	out := make(map[string]OptString, len(t))
	for k, v := range t {
		out[k] = OptString(v)
	}
	return json.Marshal(out)
}

func (t *TypeTable) UnmarshalJSON(b []byte) error {
	var raw map[string]OptString
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*t = make(TypeTable, len(raw))
	for k, v := range raw {
		(*t)[k] = string(v)
	}
	return nil
}

// Clone returns a copy of the table.
func (t TypeTable) Clone() TypeTable {
	out := make(TypeTable, len(t))
	for k, v := range t {
		out[k] = v
	}
	return out
}

// SetDefault mirrors Python's dict.setdefault: the first binding wins.
func (t TypeTable) SetDefault(name, typeName string) {
	if _, ok := t[name]; !ok {
		t[name] = typeName
	}
}

// Update overwrites the table with every binding of other.
func (t TypeTable) Update(other TypeTable) {
	for k, v := range other {
		t[k] = v
	}
}

// Has reports whether name is bound, even to an unknown type.
func (t TypeTable) Has(name string) bool {
	_, ok := t[name]
	return ok
}

// Node is one graph node: a file, a function, a method, a type, or a
// sourceless stub for a name declared in another file.
type Node struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	FileType       string `json:"file_type"`
	SourceFile     string `json:"source_file"`
	SourceLocation string `json:"source_location"`
	// Type narrows the node kind where a pass needs it ("module", "namespace",
	// "package"); empty for ordinary declarations.
	Type          string `json:"type,omitempty"`
	Callable      bool   `json:"_callable,omitempty"`
	CallableClass bool   `json:"_callable_class,omitempty"`
	GoPackagePath string `json:"_go_package_path,omitempty"`
	// Metadata carries per-language facts ({"language": "go", "kind":
	// "interface"}) and the member calls parked for a cross-repo merge
	// (UnresolvedCallsKey).
	Metadata map[string]any `json:"metadata,omitempty"`
	// GoFields rides on a Go file node: struct id -> field name -> type name.
	GoFields map[string]map[string]string `json:"_go_fields,omitempty"`
	// Origin is "ast" on everything Extract returns, so a consumer can tell
	// extracted nodes from ones added by other producers.
	Origin string `json:"_origin,omitempty"`

	// OriginFile is set on sourceless stubs while the pipeline runs: the file
	// that referenced the name. It is cleared before Extract returns.
	OriginFile string `json:"origin_file,omitempty"`
	// GoFoldedFiles lists the files whose copy of a Go type was folded into
	// this node; consumed by the id-collision pass.
	GoFoldedFiles []string `json:"-"`
}

// Edge is one graph edge.
type Edge struct {
	Source          string  `json:"source"`
	Target          string  `json:"target"`
	Relation        string  `json:"relation"`
	Context         string  `json:"context,omitempty"`
	Confidence      string  `json:"confidence"`
	ConfidenceScore float64 `json:"confidence_score,omitempty"`
	SourceFile      string  `json:"source_file"`
	SourceLocation  string  `json:"source_location"`
	Weight          float64 `json:"weight"`
	Origin          string  `json:"_origin,omitempty"`

	// TargetFile names the file an import edge resolved to, when the extractor
	// knows it. It steers the id passes and never leaves the pipeline.
	TargetFile string `json:"-"`
}

// RawCall is a call the file could not resolve on its own; the cross-file
// passes bind it.
type RawCall struct {
	CallerNID    string `json:"caller_nid"`
	Callee       string `json:"callee"`
	IsMemberCall bool   `json:"is_member_call"`
	// Indirect and Context are set for a function or method value passed as an
	// argument rather than called.
	Indirect bool   `json:"indirect,omitempty"`
	Context  string `json:"context,omitempty"`
	Language string `json:"language"`
	// Receiver is the imported package's local name, for `pkg.F()` calls only.
	Receiver       OptString `json:"receiver"`
	MemberReceiver OptString `json:"member_receiver"`
	ReceiverType   OptString `json:"receiver_type"`
	ReceiverBound  bool      `json:"receiver_bound"`
	FieldOwner     OptString `json:"field_owner"`
	ImportPath     OptString `json:"import_path"`
	SourceFile     string    `json:"source_file"`
	SourceLocation string    `json:"source_location"`
}

// GoTypeTable carries a Go file's receiver-type tables for the resolvers.
type GoTypeTable struct {
	Path        string    `json:"path"`
	Table       TypeTable `json:"table"`
	PackageVars TypeTable `json:"package_vars"`
	PackagePath OptString `json:"package_path"`
}

// FileResult is everything a language extractor reads out of one file.
type FileResult struct {
	Nodes    []*Node    `json:"nodes"`
	Edges    []*Edge    `json:"edges"`
	RawCalls []*RawCall `json:"raw_calls"`
	// Skipped marks a file the extractor declined on purpose; it yields no
	// nodes and is not a failure.
	Skipped bool `json:"skipped,omitempty"`

	// Go-specific facts the Go cross-file passes read back.
	GoImports   map[string]string `json:"go_imports,omitempty"`
	GoTypeTable *GoTypeTable      `json:"go_type_table,omitempty"`
}
