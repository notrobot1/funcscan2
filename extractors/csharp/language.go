package csharp

import "github.com/notrobot1/funcscan2/extractors/base"

// Language describes C# to the extraction pipeline.
func Language() base.Language {
	return base.Language{
		Name:       "csharp",
		Extensions: []string{".cs"},
		Extract:    ExtractCSharp,
		Resolvers: []base.Resolver{
			{Name: "csharp_member_calls", Run: resolveMemberCalls},
			{Name: "csharp_interface_dispatch", Run: resolveInterfaceDispatch},
		},
	}
}
