package extractors

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

func (t TypeTable) clone() TypeTable {
	out := make(TypeTable, len(t))
	for k, v := range t {
		out[k] = v
	}
	return out
}

// setDefault mirrors Python's dict.setdefault: the first binding wins.
func (t TypeTable) setDefault(name, typeName string) {
	if _, ok := t[name]; !ok {
		t[name] = typeName
	}
}

func (t TypeTable) update(other TypeTable) {
	for k, v := range other {
		t[k] = v
	}
}

func (t TypeTable) has(name string) bool {
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
	// OriginFile is set only on sourceless stubs: the file that referenced the name.
	OriginFile    string `json:"origin_file,omitempty"`
	Callable      bool   `json:"_callable,omitempty"`
	CallableClass bool   `json:"_callable_class,omitempty"`
	GoPackagePath string `json:"_go_package_path,omitempty"`
	// Metadata is {"language": "go", "kind": "interface"} on interface types.
	Metadata map[string]string `json:"metadata,omitempty"`
	// GoFields rides on the file node: struct id -> field name -> type name.
	GoFields map[string]map[string]string `json:"_go_fields,omitempty"`
}

// Edge is one resolved graph edge.
type Edge struct {
	Source         string  `json:"source"`
	Target         string  `json:"target"`
	Relation       string  `json:"relation"`
	Context        string  `json:"context,omitempty"`
	Confidence     string  `json:"confidence"`
	SourceFile     string  `json:"source_file"`
	SourceLocation string  `json:"source_location"`
	Weight         float64 `json:"weight"`
}

// RawCall is a call the file could not resolve on its own; a corpus-level
// resolver binds it across files.
type RawCall struct {
	CallerNID    string `json:"caller_nid"`
	Callee       string `json:"callee"`
	IsMemberCall bool   `json:"is_member_call"`
	// Indirect and Context are set for a method value passed as an argument.
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

// GoTypeTable carries the file's receiver-type tables for the resolver.
type GoTypeTable struct {
	Path        string    `json:"path"`
	Table       TypeTable `json:"table"`
	PackageVars TypeTable `json:"package_vars"`
	PackagePath OptString `json:"package_path"`
}

// Result is everything ExtractGo reads out of one .go file.
type Result struct {
	Nodes       []*Node           `json:"nodes"`
	Edges       []Edge            `json:"edges"`
	RawCalls    []RawCall         `json:"raw_calls"`
	GoImports   map[string]string `json:"go_imports"`
	GoTypeTable GoTypeTable       `json:"go_type_table"`
}
