// Package extractors turns a set of source files into one code graph.
//
// Each file is read by the extractor of its language (a subpackage such as
// extractors/go), which reports the declarations it holds, the relations it
// can prove on its own, and the calls it could not resolve. Extract then
// merges the per-file results and runs the cross-file passes: ids are made
// independent of where the checkout lives, references and calls are bound to
// the declarations they name, and each language adds the passes that follow
// its own rules.
//
// A language plugs in through base.Language. The ones this module ships are
// registered by default; Register adds more.
//
//	files, _ := extractors.CollectFiles("path/to/repo")
//	graph, err := extractors.Extract(files, extractors.Options{Root: "path/to/repo"})
package extractors

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/notrobot1/funcscan2/extractors/base"
	"github.com/notrobot1/funcscan2/extractors/csharp"
	golang "github.com/notrobot1/funcscan2/extractors/go"
	"github.com/notrobot1/funcscan2/extractors/java"
	"github.com/notrobot1/funcscan2/extractors/kotlin"
)

// The graph types live in extractors/base so that language packages can use
// them without importing this package; they are aliased here so that callers
// need only this one.
type (
	Node     = base.Node
	Edge     = base.Edge
	RawCall  = base.RawCall
	Language = base.Language
)

// Options tune one Extract run. The zero value is ready to use.
type Options struct {
	// Root is the directory ids and source files are made relative to: the
	// repository root, normally. Empty means the deepest directory that holds
	// every input file.
	Root string
	// Workers is how many files are read at once. Zero means one per CPU; one
	// reads the files in order on the calling goroutine.
	Workers int
}

// Result is the graph of one Extract run.
type Result struct {
	Nodes []*Node `json:"nodes"`
	Edges []*Edge `json:"edges"`

	// ExtractedSources are the files that were read successfully, including
	// ones their extractor skipped on purpose (SkippedSources).
	ExtractedSources []string `json:"extracted_sources"`
	SkippedSources   []string `json:"skipped_sources"`
	// FailedSources are the files that contributed nothing: the extractor
	// failed (see Errors), or it returned no nodes at all.
	FailedSources []string `json:"failed_sources"`
	// Errors holds the extractor error of each failed file.
	Errors map[string]string `json:"errors,omitempty"`
	// Warnings describe what the run could not do: files of no registered
	// language, files that failed, cross-file passes that broke.
	Warnings []string `json:"warnings,omitempty"`
}

var (
	registryMu sync.RWMutex
	languages  []*Language
	byExt      = map[string]*Language{}
)

func init() {
	// The languages this module ships. Supporting another one is a subpackage
	// that returns a base.Language, and one line here.
	Register(golang.Language())
	Register(java.Language())
	Register(csharp.Language())
	Register(kotlin.Language())
}

// Register adds a language, or replaces the one already registered under the
// same name. A file extension belongs to the language registered for it last.
func Register(lang Language) {
	if lang.Name == "" || lang.Extract == nil {
		panic("extractors: a language needs a Name and an Extract function")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	registered := &lang
	replaced := false
	for i, existing := range languages {
		if existing.Name == lang.Name {
			for ext, owner := range byExt {
				if owner == existing {
					delete(byExt, ext)
				}
			}
			languages[i] = registered
			replaced = true
		}
	}
	if !replaced {
		languages = append(languages, registered)
	}
	for _, ext := range lang.Extensions {
		byExt[ext] = registered
	}
}

// Languages lists the registered languages in registration order.
func Languages() []Language {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Language, len(languages))
	for i, lang := range languages {
		out[i] = *lang
	}
	return out
}

// Supported reports whether a registered language owns the file.
func Supported(path string) bool { return languageFor(path) != nil }

// languageFor is the language that owns the file, by extension: as written
// first, then lower-cased.
func languageFor(path string) *Language {
	ext := filepath.Ext(path)
	registryMu.RLock()
	defer registryMu.RUnlock()
	if lang, ok := byExt[ext]; ok {
		return lang
	}
	return byExt[strings.ToLower(ext)]
}

// pipeline is the state of one Extract run.
type pipeline struct {
	corpus   *base.Corpus
	result   *Result
	present  []*Language // languages that own at least one input file, in registration order
	resolved map[string]string
}

// Extract reads the files and returns their combined graph.
//
// A file that cannot be read or parsed does not fail the run: it is listed in
// Result.FailedSources and the graph is built from the rest. The error return
// is reserved for a run that could not start at all.
func Extract(paths []string, opts Options) (*Result, error) {
	p := &pipeline{
		corpus:   &base.Corpus{LanguageOf: languageFor},
		result:   &Result{Errors: map[string]string{}},
		resolved: map[string]string{},
	}
	for _, path := range paths {
		p.corpus.Paths = append(p.corpus.Paths, base.NormalizePath(path))
	}
	root := opts.Root
	if root == "" {
		root = commonRoot(p.corpus.Paths)
	}
	p.corpus.Root = p.resolve(root)

	p.extractFiles(opts.Workers)
	p.merge()

	// While every file's nodes still carry the ids the extractor minted, each
	// language folds the declarations several of its files emitted.
	p.eachLanguage("merge declarations", func(l *Language) func(*base.Corpus) { return l.MergeDeclarations })

	p.remapIDs()
	p.disambiguateCollidingIDs()

	// Ids are final. Qualified references are bound by each language's own
	// import rules before stubs are matched to definitions by bare name, so a
	// reference to an external `pkg.T` cannot collapse onto a local `T`.
	p.eachLanguage("resolve type references", func(l *Language) func(*base.Corpus) { return l.ResolveTypeReferences })
	p.rewireUniqueStubs()

	p.corpus.ResetMemo()
	p.resolveCalls()
	p.corpus.ResetMemo()
	p.eachLanguage("after calls", func(l *Language) func(*base.Corpus) { return l.AfterCalls })

	for _, lang := range p.present {
		for _, resolver := range lang.Resolvers {
			p.guard(resolver.Name, resolver.Run)
		}
	}

	p.finalize()
	p.result.Nodes, p.result.Edges = p.corpus.Nodes, p.corpus.Edges
	if len(p.result.Errors) == 0 {
		p.result.Errors = nil
	}
	return p.result, nil
}

func (p *pipeline) warnf(format string, args ...any) {
	p.result.Warnings = append(p.result.Warnings, fmt.Sprintf(format, args...))
}

// guard runs one cross-file pass. A pass that panics is reported and skipped:
// the graph is then missing what that pass would have added, nothing else.
func (p *pipeline) guard(name string, pass func(*base.Corpus)) {
	defer func() {
		if r := recover(); r != nil {
			p.warnf("%s failed, skipped: %v", name, r)
		}
		p.corpus.ResetMemo()
	}()
	pass(p.corpus)
}

func (p *pipeline) eachLanguage(stage string, hook func(*Language) func(*base.Corpus)) {
	for _, lang := range p.present {
		if pass := hook(lang); pass != nil {
			p.guard(lang.Name+": "+stage, pass)
		}
	}
}

// extractFiles runs each file through the extractor of its language.
func (p *pipeline) extractFiles(workers int) {
	paths := p.corpus.Paths
	p.corpus.PerFile = make([]*base.FileResult, len(paths))
	errs := make([]error, len(paths))

	var jobs []int
	unsupported := map[string]int{}
	for i, path := range paths {
		if languageFor(path) == nil {
			unsupported[filepath.Ext(path)]++
			continue
		}
		jobs = append(jobs, i)
	}
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if workers > len(jobs) {
		workers = len(jobs)
	}
	run := func(i int) {
		defer func() {
			if r := recover(); r != nil {
				errs[i] = fmt.Errorf("extractor panicked: %v", r)
			}
		}()
		p.corpus.PerFile[i], errs[i] = languageFor(paths[i]).Extract(paths[i])
	}
	if workers <= 1 {
		for _, i := range jobs {
			run(i)
		}
	} else {
		queue := make(chan int)
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range queue {
					run(i)
				}
			}()
		}
		for _, i := range jobs {
			queue <- i
		}
		close(queue)
		wg.Wait()
	}

	seen := map[*Language]bool{}
	for i, path := range paths {
		lang := languageFor(path)
		if lang == nil {
			p.corpus.PerFile[i] = &base.FileResult{}
			continue
		}
		if !seen[lang] {
			seen[lang] = true
		}
		result := p.corpus.PerFile[i]
		switch {
		case errs[i] != nil:
			// A failed file contributes nothing: a partial payload must not
			// leak half a file into the graph.
			p.corpus.PerFile[i] = &base.FileResult{}
			p.result.FailedSources = append(p.result.FailedSources, path)
			p.result.Errors[path] = errs[i].Error()
			p.warnf("%s: %v", path, errs[i])
		case result == nil || (len(result.Nodes) == 0 && !result.Skipped):
			// Every readable file yields at least its own file node, so no
			// nodes at all means something went wrong without an error.
			p.corpus.PerFile[i] = &base.FileResult{}
			p.result.FailedSources = append(p.result.FailedSources, path)
			p.warnf("%s: the extractor returned no nodes; the file is absent from the graph", path)
		default:
			p.result.ExtractedSources = append(p.result.ExtractedSources, path)
			if result.Skipped {
				p.result.SkippedSources = append(p.result.SkippedSources, path)
			}
		}
	}
	registryMu.RLock()
	for _, lang := range languages {
		if seen[lang] {
			p.present = append(p.present, lang)
		}
	}
	registryMu.RUnlock()
	for _, ext := range sortedKeys(unsupported) {
		p.warnf("%d %q file(s) skipped: no extractor is registered for that extension", unsupported[ext], ext)
	}
}

// merge gathers every file's nodes, edges and raw calls into the corpus.
func (p *pipeline) merge() {
	c := p.corpus
	for _, result := range c.PerFile {
		c.Nodes = append(c.Nodes, result.Nodes...)
		c.Edges = append(c.Edges, result.Edges...)
		c.RawCalls = append(c.RawCalls, result.RawCalls...)
	}
}
