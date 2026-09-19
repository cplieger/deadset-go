package deps

import (
	"go/token"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/internal/graph"
)

// The three punctuation tokens of the grammar that carry a meaning of their own.
const (
	blockOpen  = "("
	blockClose = ")"
	arrow      = "=>"
)

// The two directives a rule here reads a site for.
const (
	requireDirective = "require"
	replaceDirective = "replace"
)

// isDirective reports whether a word opens a directive. Only require and replace
// carry a site any rule here reads; the rest are named so that a line opening one
// is never read as an argument of the directive above it.
func isDirective(word string) bool {
	switch word {
	case "module", "go", "toolchain", "godebug", requireDirective, "exclude",
		replaceDirective, "retract", "tool", "ignore":
		return true
	}
	return false
}

// replaceKey identifies one replace directive by both modules it names, which is
// what tells two directives over one module apart.
type replaceKey struct {
	old         Module
	replacement Module
}

// sites holds the line each directive of one module file is written on.
//
// A position is the file's own: the name a report carries for the module file, the
// line, and the column the directive's first module path starts at, counted in
// UTF-16 code units by [graph.Column], which is the unit every position of a
// report carries.
type sites struct {
	requires map[Module]token.Position
	replaces map[replaceKey]token.Position
}

// require returns the line the require directive of module m is written on.
func (s sites) require(m Module) token.Position {
	if at, held := s.requires[m]; held {
		return at
	}
	return fileStart()
}

// replace returns the line the replace directive k names is written on.
func (s sites) replace(k replaceKey) token.Position {
	if at, held := s.replaces[k]; held {
		return at
	}
	return fileStart()
}

// fileStart is the position a directive the scan did not recognise carries. Every
// directive the toolchain printed is written somewhere in the file, so this stands
// for a spelling of the grammar the scan reads differently, and it still names the
// file a maintainer opens.
func fileStart() token.Position {
	return token.Position{Filename: modFileName, Line: 1, Column: 1}
}

// scan reads the module file's text for the line each require and each replace
// directive is written on. Where one directive is written twice, the line kept is
// the first, because the first is the one a reader finds.
func scan(text []byte) sites {
	s := sites{
		requires: make(map[Module]token.Position),
		replaces: make(map[replaceKey]token.Position),
	}
	block := ""
	for i, line := range strings.Split(string(text), "\n") {
		words := lineWords(line)
		if len(words) == 0 {
			continue
		}
		switch {
		case block == "" && !isDirective(words[0].text):
			// A line outside a block that opens no directive. The grammar has
			// none, so nothing is read from it.
		case block == "":
			if len(words) > 1 && words[1].text == blockOpen {
				block = words[0].text
				break
			}
			s.keep(words[0].text, words[1:], i+1)
		case words[0].text == blockClose:
			block = ""
		default:
			s.keep(block, words, i+1)
		}
	}
	return s
}

// keep records the line one directive of the given kind is written on, reading
// the modules it names from its arguments.
func (s sites) keep(kind string, args []word, line int) {
	if len(args) == 0 {
		return
	}
	at := token.Position{Filename: modFileName, Line: line, Column: args[0].column}
	switch kind {
	case requireDirective:
		key := moduleOf(args)
		if _, held := s.requires[key]; !held {
			s.requires[key] = at
		}
	case replaceDirective:
		key, ok := replacementOf(args)
		if !ok {
			return
		}
		if _, held := s.replaces[key]; !held {
			s.replaces[key] = at
		}
	}
}

// moduleOf reads the module one directive's arguments name: the path, and the
// version where the directive gives one.
func moduleOf(args []word) Module {
	m := Module{Path: args[0].text}
	if len(args) > 1 {
		m.Version = args[1].text
	}
	return m
}

// replacementOf reads the two modules one replace directive names, and reports
// whether its arguments carry both.
func replacementOf(args []word) (replaceKey, bool) {
	at := -1
	for i, a := range args {
		if a.text == arrow {
			at = i
			break
		}
	}
	if at < 1 || at == len(args)-1 {
		return replaceKey{}, false
	}
	return replaceKey{old: moduleOf(args[:at]), replacement: moduleOf(args[at+1:])}, true
}

// word is one word of a module file line and the column it starts at.
type word struct {
	text   string
	column int
}

// lineWords splits one line into the words the grammar reads, dropping the
// comment a line may end with. A quoted word comes back unquoted, which is the
// spelling the toolchain prints, and a quotation that does not close comes back as
// it is written, so a line the grammar refuses is never read as something else.
//
// A word's column is the graph's own, so a module path written after text outside
// the Basic Multilingual Plane is positioned the way every other position of a
// report is.
func lineWords(line string) []word {
	var found []word
	source := []byte(line)
	for i := 0; i < len(line); {
		switch {
		case line[i] == ' ' || line[i] == '\t' || line[i] == '\r':
			i++
		case strings.HasPrefix(line[i:], "//"):
			return found
		default:
			text, width := readWord(line[i:])
			found = append(found, word{text: text, column: graph.Column(source, i)})
			i += width
		}
	}
	return found
}

// readWord returns the word at the start of s and how many bytes of s it
// occupies.
func readWord(s string) (text string, width int) {
	if s[0] == '"' || s[0] == '`' {
		if quoted, ok := quotation(s); ok {
			if unquoted, err := strconv.Unquote(quoted); err == nil {
				return unquoted, len(quoted)
			}
			return quoted, len(quoted)
		}
	}
	end := strings.IndexAny(s, " \t\r")
	if end < 0 {
		end = len(s)
	}
	return s[:end], end
}

// quotation returns the quoted word at the start of s, and reports whether the
// quotation closes. A raw quotation ends at the next backquote, an interpreted
// one at the next quotation mark no reverse solidus escapes.
func quotation(s string) (string, bool) {
	if s[0] == '`' {
		if end := strings.IndexByte(s[1:], '`'); end >= 0 {
			return s[:end+2], true
		}
		return "", false
	}
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return s[:i+1], true
		}
	}
	return "", false
}
