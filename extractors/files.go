package extractors

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// skipDirs are directories that never hold a project's own source: dependency
// trees, virtual environments, caches and build output.
var skipDirs = map[string]bool{
	"venv": true, ".venv": true,
	"node_modules": true, "__pycache__": true, ".git": true,
	"dist": true, "build": true, "target": true,
	"site-packages": true, "lib64": true,
	".pytest_cache": true, ".mypy_cache": true, ".ruff_cache": true,
	".tox": true, ".nox": true, ".eggs": true,
	"lcov-report": true, "visual-tests": true, "visual-test": true,
	"__snapshots__": true, "storybook-static": true, "dist-protected": true,
	".next": true, ".nuxt": true, ".turbo": true, ".angular": true,
	".idea": true, ".cache": true, ".parcel-cache": true, ".svelte-kit": true,
	".terraform": true, ".serverless": true,
	".obsidian": true, ".smart-env": true, ".worktrees": true,
}

func isNoiseDir(name string) bool {
	return skipDirs[name] || strings.HasSuffix(name, "_venv") || strings.HasSuffix(name, ".egg-info")
}

// CollectFiles lists the files under target that a registered language can
// extract, sorted. target may also be a single file, which is returned as is.
//
// Dependency, cache and build directories (node_modules, .git, dist, …) are
// not entered, and directory symlinks are not followed. A file symlink is
// taken only when it points inside target, so a scan cannot wander out of the
// tree it was asked about.
func CollectFiles(target string) ([]string, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{target}, nil
	}
	root, err := filepath.EvalSymlinks(target)
	if err != nil {
		return nil, err
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}

	var files []string
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return // unreadable directory: skip it, keep going
		}
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			switch {
			case entry.IsDir():
				if !isNoiseDir(entry.Name()) {
					walk(path)
				}
			case !Supported(path):
			case entry.Type()&fs.ModeSymlink != 0:
				real, err := filepath.EvalSymlinks(path)
				if err != nil {
					continue
				}
				if abs, err := filepath.Abs(real); err == nil {
					real = abs
				}
				if _, inside := relativeTo(real, root); inside && isFile(real) {
					files = append(files, path)
				}
			case entry.Type().IsRegular():
				files = append(files, path)
			}
		}
	}
	walk(target)
	// Component by component, so a directory's files stay together whatever
	// characters its siblings' names hold.
	sort.Slice(files, func(i, j int) bool {
		return slices.Compare(strings.Split(files[i], sep), strings.Split(files[j], sep)) < 0
	})
	return files, nil
}
