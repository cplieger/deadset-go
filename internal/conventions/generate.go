package conventions

import (
	"go/ast"
	"iter"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// The spellings the go:generate reader reads.
const (
	generateDirective = "//go:generate "
	currentPackage    = "."
	goCommand         = "go"
	sourceFlag        = "source"
	typeFlag          = "type"
)

// goValueFlags are the build flags of go run that take a value, which may be the next
// word: https://pkg.go.dev/cmd/go#hdr-Compile_packages_and_dependencies
var goValueFlags = []string{
	"C", "asmflags", "buildmode", "compiler", "covermode", "coverpkg", "exec", "gccgoflags", "gcflags",
	"installsuffix", "ldflags", "mod", "modfile", "overlay", "p", "pgo", "pkgdir", "tags", "toolexec",
}

// Directives yields the words of every go:generate directive of f, the file at
// file of package pkg, as go generate splits and expands them.
func Directives(f *ast.File, file, pkg string) iter.Seq[[]string] {
	return func(yield func([]string) bool) {
		for _, group := range f.Comments {
			for _, c := range group.List {
				if line, ok := strings.CutPrefix(c.Text, generateDirective); ok && !yield(generateWords(line, filepath.Base(file), pkg)) {
					return
				}
			}
		}
	}
}

// generateWords splits one directive the way go generate does, at spaces and tabs with a
// double-quoted Go string one word, then expands the file and package variables in
// each word: https://pkg.go.dev/cmd/go#hdr-Generate_Go_files_by_processing_source
func generateWords(line, file, pkg string) []string {
	expand := strings.NewReplacer("$GOFILE", file, "${GOFILE}", file, "$GOPACKAGE", pkg, "${GOPACKAGE}", pkg)
	var words []string
	for line = strings.TrimLeft(line, " \t"); line != ""; line = strings.TrimLeft(line, " \t") {
		word, rest := line, ""
		if line[0] == '"' {
			end := closingQuote(line)
			unquoted, err := strconv.Unquote(line[:end])
			if err != nil {
				return nil
			}
			word, rest = unquoted, line[end:]
		} else if at := strings.IndexAny(line, " \t"); at >= 0 {
			word, rest = line[:at], line[at:]
		}
		words = append(words, expand.Replace(word))
		line = rest
	}
	return words
}

// closingQuote is the length of the double-quoted Go string line starts with, through its
// closing quote, or the whole line where none closes it.
func closingQuote(line string) int {
	for at := 1; at < len(line); at++ {
		switch line[at] {
		case '\\':
			at++
		case '"':
			return at + 1
		}
	}
	return len(line)
}

// generatorOf is the base name of the program one directive runs, without a module
// version, and the words it passes it: `go run <package>` and `go tool <name>` run
// the program they name.
func generatorOf(words []string) (name string, args []string) {
	if len(words) == 0 {
		return "", nil
	}
	program, args := words[0], words[1:]
	if program == goCommand && len(args) > 1 && (args[0] == "run" || args[0] == "tool") {
		at := 1
		for at < len(args) && strings.HasPrefix(args[at], "-") {
			flag, _, joined := strings.Cut(strings.TrimLeft(args[at], "-"), "=")
			if !joined && slices.Contains(goValueFlags, flag) {
				at++
			}
			at++
		}
		if at >= len(args) {
			return "", nil
		}
		program, args = args[at], args[at+1:]
	}
	program, _, _ = strings.Cut(program, "@")
	return path.Base(filepath.ToSlash(program)), args
}

// flagValue is the value one flag carries among args, in its -name=value or
// -name value form, and the empty string where args do not set it.
func flagValue(args []string, name string, boolean []string) string {
	for i := 0; i < len(args); i++ {
		flag, value, joined := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if !strings.HasPrefix(args[i], "-") || slices.Contains(boolean, flag) {
			continue
		}
		if !joined {
			value = ""
			// A boolean flag written before another flag takes no value.
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				value = args[i+1]
				i++
			}
		}
		if flag == name {
			return value
		}
	}
	return ""
}

// positionalArgs is every word of args that is neither a flag nor a flag's value.
func positionalArgs(args, boolean []string) []string {
	var held []string
	for i := 0; i < len(args); i++ {
		word := args[i]
		if !strings.HasPrefix(word, "-") {
			held = append(held, word)
			continue
		}
		flag, _, joined := strings.Cut(strings.TrimLeft(word, "-"), "=")
		if !joined && !slices.Contains(boolean, flag) && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			i++
		}
	}
	return held
}

// docLines yields every line of a doc comment with its comment markers and its
// leading spaces, tabs and asterisks removed. The raw comments are read because
// [ast.CommentGroup.Text] drops a line spelled as a directive, as one written with
// no space after the slashes is.
func docLines(doc *ast.CommentGroup) iter.Seq[string] {
	return func(yield func(string) bool) {
		if doc == nil {
			return
		}
		for _, c := range doc.List {
			text := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(c.Text, "//"), "/*"), "*/")
			for line := range strings.SplitSeq(text, "\n") {
				if !yield(strings.TrimLeft(line, " \t*")) {
					return
				}
			}
		}
	}
}
