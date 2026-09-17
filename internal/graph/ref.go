package graph

import "strings"

// refPrefix namespaces every reference this analyzer emits by language.
const refPrefix = "go://"

// Ref returns the stable symbol reference of one symbol: the language prefix,
// the import path of its package, and the fragment its kind spells. chain is the
// name chain from the package inward with the symbol's own name last, and is
// empty for a package.
//
// Nothing derived from a package identifier, a file path or a line enters the
// result. A type parameter is the reference of the function or the method that
// declares it, followed by the parameter's own name in square brackets.
func Ref(kind SymbolKind, pkgPath string, chain []string) string {
	if len(chain) == 0 {
		return refPrefix + pkgPath + "#"
	}
	own := chain[len(chain)-1]

	switch kind {
	case KindPackage:
		return refPrefix + pkgPath + "#"
	case KindFile:
		return refPrefix + pkgPath + "#" + own + ":file"
	case KindTypeParam:
		owner := strings.Join(chain[:len(chain)-1], ".")
		return refPrefix + pkgPath + "#" + owner + "[" + own + "]"
	default:
		return refPrefix + pkgPath + "#" + strings.Join(chain, ".")
	}
}
