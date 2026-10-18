package suppress

import (
	"regexp"
	"strings"
)

// referenceBlock is one named building block the grammar of the stable symbol
// reference is written from.
type referenceBlock struct {
	name       string
	expression string
}

// referenceBlocks are those blocks. An identifier character outside ASCII is
// written as the whole non-ASCII range, so an expression is a form checker and
// not an identifier validator: the compiler that accepted the declaration is
// what validated its name.
var referenceBlocks = [...]referenceBlock{
	{"GO_IDENT", `(?:[A-Za-z_]|[^\x00-\x7F])(?:[A-Za-z0-9_]|[^\x00-\x7F])*`},
	{"GO_PATH", `[A-Za-z0-9._~-]+(?:/[A-Za-z0-9._~-]+)*`},
	{"GO_VERSION", `[A-Za-z0-9.+-]+`},
	{"GO_FILE", `[^/\\:#\r\n]+\.go`},
	{"TS_IDENT", `(?:[A-Za-z_$]|[^\x00-\x7F])(?:[A-Za-z0-9_$]|[^\x00-\x7F])*`},
	{"TS_QUOTED", `'(?:[^'\\\r\n]|\\['\\nr])*'`},
	{"TS_COMPUTED", `\[[^\]\r\n\t ]+\]`},
	{"TS_HEAD", `(?:TS_IDENT|TS_QUOTED)`},
	{"TS_MEMBER", `(?:TS_IDENT|#TS_IDENT|TS_QUOTED|TS_COMPUTED)`},
	{"TS_PACKAGE", `(?:@[A-Za-z0-9._-]+/)?[A-Za-z0-9._-]+`},
	{"TS_FILE", `(?:[^/#\r\n]+/)*[^/#\r\n]+(?:(?:\.d)?\.[mc]?ts|\.tsx|\.[mc]?js|\.jsx)`},
	{"PROTO_IDENT", `[A-Za-z_][A-Za-z0-9_]*`},
	{"PROTO_PACKAGE", `(?:\.|PROTO_IDENT(?:\.PROTO_IDENT)*)`},
	{"PROTO_PATH", `(?:[^/#:\r\n]+/)*[^/#:\r\n]+\.proto`},
}

// referenceForms is one template per form the grammar fixes, in the grammar's
// own order: the Go forms, then the TypeScript ones, then the Protocol Buffers
// ones. Several forms of the vocabulary share one template, because they differ
// in what the symbol is rather than in how the reference is spelled.
var referenceForms = [...]string{
	`^go://GO_PATH#$`,
	`^go://GO_PATH#GO_IDENT$`,
	`^go://GO_PATH#GO_IDENT(?:\.GO_IDENT)+$`,
	`^go://GO_PATH#GO_IDENT(?:\.GO_IDENT)?\[GO_IDENT\]$`,
	`^go://GO_PATH#(?:GO_IDENT(?:\.GO_IDENT)?)?\(GO_IDENT\)$`,
	`^go://GO_PATH#GO_FILE:file$`,
	`^go://GO_PATH#GO_PATH(?:@GO_VERSION)?:require$`,
	`^go://GO_PATH#GO_PATH(?:@GO_VERSION)?:replace$`,
	`^ts://TS_PACKAGE/TS_FILE#$`,
	`^ts://TS_PACKAGE/TS_FILE#TS_HEAD$`,
	`^ts://TS_PACKAGE/TS_FILE#(?:TS_QUOTED\.)?TS_HEAD:alias$`,
	`^ts://TS_PACKAGE/TS_FILE#TS_HEAD:import$`,
	`^ts://TS_PACKAGE/TS_FILE#TS_HEAD(?:\.TS_MEMBER)+$`,
	`^ts://TS_PACKAGE/TS_FILE#TS_HEAD(?:\.TS_MEMBER)+:static$`,
	`^ts://TS_PACKAGE/TS_FILE#TS_HEAD(?:\.TS_MEMBER)+\.TS_QUOTED:case$`,
	`^ts://TS_PACKAGE/TS_FILE#TS_HEAD(?:\.TS_MEMBER)*(?::static)?<TS_IDENT>$`,
	`^ts://TS_PACKAGE/TS_FILE#(?:TS_HEAD(?:\.TS_MEMBER)*(?::static)?)?\(TS_IDENT\)$`,
	`^ts://TS_PACKAGE/(?:[^/#\r\n]+/)*package\.json#TS_PACKAGE:(?:dependency|dev-dependency|peer-dependency)$`,
	`^proto://PROTO_PACKAGE#PROTO_IDENT(?:\.PROTO_IDENT)*$`,
	`^proto://PROTO_PACKAGE#PROTO_PATH:file$`,
}

// referenceExpressions are the forms with every block substituted, compiled once.
var referenceExpressions = compileReferences()

// expandReference substitutes every block into one template, repeatedly, because
// two blocks are themselves written from others. No block's name is a prefix of
// another's, so the substitution has one result whatever order it runs in.
func expandReference(template string) string {
	for range referenceBlocks {
		expanded := template
		for _, b := range referenceBlocks {
			expanded = strings.ReplaceAll(expanded, b.name, b.expression)
		}
		if expanded == template {
			return template
		}
		template = expanded
	}
	return template
}

// compileReferences compiles every form. Each template is a constant of this
// file, so a template that does not compile is a defect in the transcription and
// not a document a run can be given.
func compileReferences() []*regexp.Regexp {
	compiled := make([]*regexp.Regexp, 0, len(referenceForms))
	for _, form := range referenceForms {
		compiled = append(compiled, regexp.MustCompile(expandReference(form)))
	}
	return compiled
}

// referenceForm reports whether ref is a stable symbol reference of some form the
// grammar fixes. Every language's forms are checked: one target holds one ignore
// file, whose entries may name symbols of any of them, and an entry naming a symbol
// this analysis enumerates nothing under binds nothing rather than being refused.
func referenceForm(ref string) bool {
	for _, re := range referenceExpressions {
		if re.MatchString(ref) {
			return true
		}
	}
	return false
}
