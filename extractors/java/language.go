package java

import "github.com/notrobot1/funcscan2/extractors/base"

// Language describes Java to the extraction pipeline.
// Java and Kotlin share the "jvm" family so cross-language calls can bind.
func Language() base.Language {
	return base.Language{
		Name:       "java",
		Family:     "jvm",
		Extensions: []string{".java"},
		Extract:    ExtractJava,
		Resolvers: []base.Resolver{
			{Name: "java_member_calls", Run: resolveMemberCalls},
			{Name: "java_interface_dispatch", Run: resolveInterfaceDispatch},
		},
	}
}
