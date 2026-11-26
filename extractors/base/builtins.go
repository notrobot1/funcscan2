package base

// LanguageBuiltinGlobals are language built-in globals that an AST may classify
// as call targets when used as constructors or coercion functions (String(x),
// Number(x), Boolean(x)). Without this filter they become god-nodes
// accumulating spurious edges from every call site. The set is shared by every
// language's extractor; Go's own predeclared functions are handled separately
// inside the Go extractor.
var LanguageBuiltinGlobals = newStringSet(
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
)

func newStringSet(items ...string) map[string]struct{} {
	s := make(map[string]struct{}, len(items))
	for _, it := range items {
		s[it] = struct{}{}
	}
	return s
}
