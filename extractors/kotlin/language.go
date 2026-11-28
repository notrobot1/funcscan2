package kotlin

import "github.com/notrobot1/funcscan2/extractors/base"

// Language describes Kotlin to the extraction pipeline.
// Kotlin and Java share the "jvm" family so cross-language calls can bind.
func Language() base.Language {
	return base.Language{
		Name:       "kotlin",
		Family:     "jvm",
		Extensions: []string{".kt", ".kts"},
		Extract:    ExtractKotlin,
		Resolvers: []base.Resolver{
			{Name: "kotlin_import_targets", Run: resolveImportTargets},
			{Name: "kotlin_member_calls", Run: resolveMemberCalls},
			{Name: "kotlin_interface_dispatch", Run: resolveInterfaceDispatch},
		},
	}
}
