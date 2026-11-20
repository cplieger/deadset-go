package inventory

import (
	"strings"

	"github.com/cplieger/deadset-spec/v8/segment"
)

// refPrefix namespaces every reference this analyzer emits by language.
const refPrefix = "go://"

// Ref returns the stable symbol reference of one symbol: the language prefix,
// the import path of its package, and the fragment its kind spells. chain is the
// name chain from the package inward with the symbol's own name last, and is
// empty for a package. Each import path element and a file's name are spelled as
// the grammar's Path segments section states, so a byte outside the element's or
// the file operand's set, such as the + of a directory c++, is percent-encoded.
//
// Nothing derived from a package identifier, a file path or a line enters the
// result. A type parameter is the reference of the function or the method that
// declares it, followed by the parameter's own name in square brackets.
func Ref(kind SymbolKind, pkgPath string, chain []string) string {
	scope := refPrefix + segment.SpellPath(segment.GoElement, pkgPath) + "#"
	if len(chain) == 0 {
		return scope
	}
	own := chain[len(chain)-1]

	switch kind {
	case KindPackage:
		return scope
	case KindFile:
		return scope + segment.Spell(segment.FileOperand, own) + ":file"
	case KindTypeParam:
		owner := strings.Join(chain[:len(chain)-1], ".")
		return scope + owner + "[" + own + "]"
	default:
		return scope + strings.Join(chain, ".")
	}
}

// ParameterRef returns the reference of a parameter: the reference of the
// declaration that holds it, a function literal's parameter included, followed by
// the parameter's name in parentheses.
func ParameterRef(holder, name string) string {
	return holder + "(" + name + ")"
}

// ParameterHolder returns the reference of the declaration a parameter's reference
// spells it under, and ref itself with false where ref names no parameter. Only the
// parameter form ends with a closing parenthesis, so the last opening one starts it.
func ParameterHolder(ref string) (string, bool) {
	open := strings.LastIndexByte(ref, '(')
	if open < 0 || !strings.HasSuffix(ref, ")") {
		return ref, false
	}
	return ref[:open], true
}
