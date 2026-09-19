package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"pgregory.net/rapid"
)

// The four places a reference to a generated declaration comes from. Each produces a
// different state of the analysis, which is what makes a generated module exercise
// more than one answer.
const (
	// useNone is a declaration nothing references, which the sweep judges dead and
	// a kind reports.
	useNone = "none"

	// useProduction is a declaration the entry point references, which both
	// liveness relations hold live.
	useProduction = "production"

	// useTest is a declaration a test file alone references, which a production
	// sweep judges dead and the test-only kind reports.
	useTest = "test"

	// useCascade is a declaration one unreferenced declaration references, which is
	// live under reference counting, dead under reachability, and a member of the
	// dead component whose root is the declaration that references it.
	useCascade = "cascade"
)

// drawnDeclaration is one declaration of a generated module: the name it is declared
// under, and where the reference to it comes from.
type drawnDeclaration struct {
	name string
	use  string
}

// drawnModule is one generated target tree: the files it holds, the declarations it
// declares, and whether it declares a method satisfying a standard interface.
type drawnModule struct {
	files     map[string]string
	decls     []drawnDeclaration
	satisfies bool
}

// drawnModules is drawModule as a generator, so that a property whose cost is the
// number of modules it analyzes can take a fixed set of them from the generator's own
// examples instead of drawing one per iteration.
func drawnModules(document string, exportedFirst bool) *rapid.Generator[drawnModule] {
	return rapid.Custom(func(t *rapid.T) drawnModule {
		return drawModule(t, document, exportedFirst)
	})
}

// drawModule draws one target tree: a main package with an entry point, a few
// declarations each referenced from the entry point, from a test file alone, from one
// unreferenced declaration or from nothing, and a type whose method satisfies
// io.Writer when drawn.
//
// The four reference sites are what make one generated module exercise more than one
// state of the analysis, and the satisfying method is what makes an exemption class
// hold something back, which no reference structure alone produces.
//
// exportedFirst forces the first declaration to be exported and unreferenced, for a
// property whose subject is the treatment of a published API.
func drawModule(t *rapid.T, document string, exportedFirst bool) drawnModule {
	count := rapid.IntRange(1, 4).Draw(t, "the number of declarations")
	uses := []string{useNone, useProduction, useTest, useCascade}
	decls := make([]drawnDeclaration, count)
	for i := range count {
		label := "declaration " + strconv.Itoa(i)
		exported := rapid.Bool().Draw(t, label+": exported")
		use := rapid.SampledFrom(uses).Draw(t, label+": where the reference comes from")
		if i == 0 && exportedFirst {
			exported, use = true, useNone
		}
		name := "d" + strconv.Itoa(i)
		if exported {
			name = "D" + strconv.Itoa(i)
		}
		decls[i] = drawnDeclaration{name: name, use: use}
	}

	drawn := drawnModule{
		decls:     decls,
		satisfies: rapid.Bool().Draw(t, "a method satisfying a standard interface"),
	}
	drawn.files = map[string]string{
		"go.mod":           "module example.com/app\n\ngo 1.27.1\n",
		"app.go":           drawn.source(),
		repositoryDocument: document,
	}
	if test := drawn.testSource(); test != "" {
		drawn.files["app_test.go"] = test
	}
	return drawn
}

// used is every declaration the draw put at one reference site, in the draw's order.
func (d *drawnModule) used(use string) []drawnDeclaration {
	var held []drawnDeclaration
	for _, one := range d.decls {
		if one.use == use {
			held = append(held, one)
		}
	}
	return held
}

// source is the module's one production file.
func (d *drawnModule) source() string {
	var held strings.Builder
	held.WriteString("// Command app is a generated target.\npackage main\n\n")
	if d.satisfies {
		held.WriteString("import (\n\t\"io\"\n\t\"strings\"\n)\n\n")
	}

	held.WriteString("func main() {\n")
	for _, one := range d.used(useProduction) {
		fmt.Fprintf(&held, "\t%s()\n", one.name)
	}
	if d.satisfies {
		held.WriteString("\tvar s sink\n\tif _, err := io.Copy(&s, strings.NewReader(\"x\")); err != nil {\n")
		held.WriteString("\t\tpanic(err)\n\t}\n")
	}
	held.WriteString("}\n")

	for _, one := range d.decls {
		fmt.Fprintf(&held, "\n// %s is a generated declaration.\nfunc %s() {}\n", one.name, one.name)
	}
	if cascaded := d.used(useCascade); len(cascaded) > 0 {
		held.WriteString("\n// orphan references declarations nothing else references, and nothing\n")
		held.WriteString("// references orphan.\nfunc orphan() {\n")
		for _, one := range cascaded {
			fmt.Fprintf(&held, "\t%s()\n", one.name)
		}
		held.WriteString("}\n")
	}
	if d.satisfies {
		held.WriteString("\n// sink counts the bytes written to it.\ntype sink struct{ n int }\n")
		held.WriteString("\n// Write satisfies io.Writer and is called through that interface alone.\n")
		held.WriteString("func (s *sink) Write(p []byte) (int, error) {\n\ts.n += len(p)\n\treturn len(p), nil\n}\n")
	}
	return held.String()
}

// testSource is the module's test file, and the empty string where the draw put no
// declaration behind a test reference.
func (d *drawnModule) testSource() string {
	tested := d.used(useTest)
	if len(tested) == 0 {
		return ""
	}
	var held strings.Builder
	held.WriteString("package main\n\nimport \"testing\"\n\nfunc TestGenerated(t *testing.T) {\n")
	for _, one := range tested {
		fmt.Fprintf(&held, "\t%s()\n", one.name)
	}
	held.WriteString("}\n")
	return held.String()
}

// writeFiles writes one generated tree under dir, creating the directories its paths
// name. It takes no testing value, because it is called from inside a property.
func writeFiles(dir string, files map[string]string) error {
	for base, body := range files {
		path := filepath.Join(dir, base)
		if parent := filepath.Dir(path); parent != dir {
			if err := os.MkdirAll(parent, 0o750); err != nil {
				return fmt.Errorf("create %s: %w", parent, err)
			}
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}
