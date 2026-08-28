package security

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Guards that enumerate what they cover stop covering things.
//
// The guards written to catch "a guard on one half of a boundary" listed their own
// files by hand: four paths for the route check, three directories for the
// intelligence-plane check, three files for the evidence-payload check. A new package
// with an unauthenticated route and a forbidden import was invisible to all three, and
// every one of them stayed green. The pattern reproduced itself one level up, in the
// code written to stop it.
//
// So nothing here is listed. The repository is walked, and a guard that finds nothing
// fails rather than passing quietly.

const repoRoot = "../.."

// sourceDirs are the trees a guard may care about. Directories, not files: a new file
// inside one is found, and a new tree outside them is a deliberate act.
var sourceDirs = []string{"cmd", "internal", "adapters"}

// goSources walks the repository and returns every non-test Go file.
func goSources(t *testing.T) []string {
	t.Helper()

	var files []string
	for _, dir := range sourceDirs {
		root := filepath.Join(repoRoot, dir)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := d.Name()
			if d.IsDir() {
				if name == "testdata" || name == "node_modules" {
					return fs.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	if len(files) < 20 {
		t.Fatalf("found only %d Go files under %v; the walk is looking in the wrong "+
			"place and every guard built on it would pass over an empty set",
			len(files), sourceDirs)
	}
	return files
}

// packageOf returns the import path a file belongs to, as the module spells it.
func packageOf(path string) string {
	rel, err := filepath.Rel(repoRoot, filepath.Dir(path))
	if err != nil {
		return ""
	}
	return "agentic-assurance/" + filepath.ToSlash(rel)
}

// localImports returns the module-local packages a file imports.
func localImports(t *testing.T, path string) []string {
	t.Helper()

	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var out []string
	for _, spec := range parsed.Imports {
		imported := strings.Trim(spec.Path.Value, `"`)
		if strings.HasPrefix(imported, "agentic-assurance/") {
			out = append(out, imported)
		}
	}
	return out
}

// dependencyClosure returns every module-local package a root package reaches,
// transitively.
//
// Transitive because a process is bounded by everything it can reach, not by what its
// own files happen to name. A binary that imports one intelligence package which
// imports the authority store has the authority store in it.
func dependencyClosure(t *testing.T, root string) map[string]bool {
	t.Helper()

	byPackage := map[string][]string{}
	for _, file := range goSources(t) {
		pkg := packageOf(file)
		byPackage[pkg] = append(byPackage[pkg], file)
	}
	if _, ok := byPackage[root]; !ok {
		t.Fatalf("package %s was not found by the walk; the guard is pointed at "+
			"nothing", root)
	}

	seen := map[string]bool{}
	var visit func(string)
	visit = func(pkg string) {
		if seen[pkg] {
			return
		}
		seen[pkg] = true
		for _, file := range byPackage[pkg] {
			for _, imported := range localImports(t, file) {
				visit(imported)
			}
		}
	}
	visit(root)
	delete(seen, root)
	return seen
}

// readSource is os.ReadFile with the failure spelled out.
func readSource(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// walkTypeScript collects .ts and .tsx files, skipping build output.
func walkTypeScript(t *testing.T, root string, into *[]string) {
	t.Helper()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".next", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".ts") || strings.HasSuffix(d.Name(), ".tsx") {
			*into = append(*into, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}
