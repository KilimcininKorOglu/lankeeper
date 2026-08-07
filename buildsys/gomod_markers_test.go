package buildsys

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// requireLine captures a module path, its version and whether the line
// carries the indirect marker.
var requireLine = regexp.MustCompile(`^\s*([\w.\-]+\.[\w.\-]+/\S*)\s+v\S+(\s*//\s*indirect)?\s*$`)

// indirectMarkers reads go.mod and reports, per module path, whether it
// is marked indirect.
func indirectMarkers(t *testing.T) map[string]bool {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}

	out := make(map[string]bool)
	for line := range strings.SplitSeq(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "module ") {
			continue
		}
		if m := requireLine.FindStringSubmatch(line); m != nil {
			out[m[1]] = strings.TrimSpace(m[2]) != ""
		}
	}
	if len(out) == 0 {
		t.Fatal("parsed no require lines out of go.mod")
	}
	return out
}

// productionImports walks the non-test Go files and returns every
// import path they name. Test-only dependencies are legitimately
// indirect, so they must not count.
func productionImports(t *testing.T) map[string]string {
	t.Helper()

	out := make(map[string]string)
	fset := token.NewFileSet()

	for _, root := range []string{"../internal", "../cmd", "../buildsys"} {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			f, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if parseErr != nil {
				return parseErr
			}
			for _, imp := range f.Imports {
				out[strings.Trim(imp.Path.Value, `"`)] = path
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	return out
}

// TestNoDirectDependencyIsMarkedIndirect is the regression test. Two
// modules imported by production code sat in the indirect block, having
// been left there when the lease watcher and the SFTP backup target
// were added.
//
// Module resolution ignores the comment, so nothing built differently.
// It misleads a reader, and any tooling that derives the direct set
// from the marker rather than from the import graph, which can make a
// genuinely required dependency look droppable during a dependency
// review. `go mod tidy` would fix it, but no gate runs tidy, so nothing
// noticed for two releases.
func TestNoDirectDependencyIsMarkedIndirect(t *testing.T) {
	markers := indirectMarkers(t)
	imports := productionImports(t)

	for module, indirect := range markers {
		if !indirect {
			continue
		}
		for imported, file := range imports {
			if imported == module || strings.HasPrefix(imported, module+"/") {
				t.Errorf("%s is marked indirect but %s imports %s", module, file, imported)
				break
			}
		}
	}
}

// TestEveryDirectDependencyIsImported is the other half. An entry that
// lost its marker without anything importing it is the same mistake
// pointing the other way, and it keeps a dependency alive that a review
// would otherwise drop.
func TestEveryDirectDependencyIsImported(t *testing.T) {
	markers := indirectMarkers(t)
	imports := productionImports(t)

	for module, indirect := range markers {
		if indirect {
			continue
		}

		var used bool
		for imported := range imports {
			if imported == module || strings.HasPrefix(imported, module+"/") {
				used = true
				break
			}
		}
		if !used {
			t.Errorf("%s sits in the direct require block but no production file imports it", module)
		}
	}
}
