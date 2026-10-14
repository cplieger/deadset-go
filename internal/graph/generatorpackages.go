package graph

import (
	"go/ast"
	"path"
	"path/filepath"
	"strings"
)

// generatorPackages records the directory of every package of the target that a
// directive of f passes, by a relative path, to a generator that loads the package and
// reads its declarations by their names.
func (d *rootDetection) generatorPackages(f *ast.File) {
	file := d.pos.fset.Position(f.FileStart).Filename
	for _, group := range f.Comments {
		for _, c := range group.List {
			if line, ok := strings.CutPrefix(c.Text, generateDirective); ok {
				d.packageArguments(file, generateWords(line, filepath.Base(file), f.Name.Name))
			}
		}
	}
}

// packageGenerators names each generator that loads a package passed by a relative
// path and reads its declarations by name, with the subcommand whose positional
// arguments are those packages: ent's "generate" (https://entgo.io/docs/code-gen).
// Another generator's relative path may as well be where it writes, so it names no input.
var packageGenerators = map[string]string{"ent": "generate", "entc": "generate"}

// packageArguments records the directory of every package below the target that one
// directive's words pass by a relative path to a generator packageGenerators names,
// the directive's own directory excepted.
func (d *rootDetection) packageArguments(file string, words []string) {
	name, args := generatorOf(words)
	subcommand, ok := packageGenerators[name]
	if !ok || len(args) == 0 || args[0] != subcommand {
		return
	}
	for _, arg := range positionalArgs(args[1:], nil) {
		if !strings.HasPrefix(arg, "./") && !strings.HasPrefix(arg, "../") {
			continue
		}
		dir := filepath.Join(filepath.Dir(file), filepath.FromSlash(arg))
		if relative := d.pos.relative(dir); relative != "" && dir != filepath.Dir(file) {
			d.inputDirs[relative] = true
		}
	}
}

// generatorPackageInputs roots every declaration a package generatorPackages recorded
// exports to a generator: an exported declaration outside a test file whose every
// container is exported too, a type parameter excepted.
func (d *rootDetection) generatorPackageInputs(symbols []Symbol) {
	if len(d.inputDirs) == 0 {
		return
	}
	for i := range symbols {
		s := &symbols[i]
		_, test := IsTestFile(s.Pos.Filename)
		if d.inputDirs[path.Dir(s.Pos.Filename)] && s.Exported && s.Kind != KindTypeParam && !test && d.exportedContainers(s.Parent) {
			d.add(s.ID, RootGenerate, "")
		}
	}
}
