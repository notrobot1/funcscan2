package golang

import "github.com/notrobot1/funcscan2/extractors/base"

// Language describes Go to the extraction pipeline: the per-file extractor
// plus the cross-file passes that follow Go's own rules (packages spread over
// files, import paths from go.mod, interfaces satisfied implicitly).
func Language() base.Language {
	return base.Language{
		Name:                  "go",
		Extensions:            []string{".go"},
		Extract:               ExtractGo,
		MergeDeclarations:     mergePackageTypes,
		ResolveTypeReferences: resolveTypeReferences,
		SkipCall:              skipCall,
		FilterCallCandidates:  filterCallCandidates,
		AfterCalls:            repointImports,
		Resolvers: []base.Resolver{
			{Name: "go_member_calls", Run: resolveMemberCalls},
			{Name: "go_interface_dispatch", Run: resolveInterfaceDispatch},
		},
	}
}
