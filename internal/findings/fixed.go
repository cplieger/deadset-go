package findings

import (
	"go/types"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
)

// readFixed adds to fixed every method of the target one configuration defines
// whose name and signature are those of a method [facts.Index.OutsideMethods]
// holds, a loaded consumer's among them. That interface fixes the method's
// signature and its exportedness whether or not the program converts the method's
// type to it, so the method is neither narrowed nor judged for its parameters and
// results.
func readFixed(one *Configured, fixed map[inventory.SymbolID]bool) {
	if one.Result == nil || one.Resolve == nil || one.Facts == nil {
		return
	}
	for _, p := range one.Facts.Packages() {
		for _, object := range p.TypesInfo.Defs {
			if id, isFixed := fixedMethod(one, object); isFixed {
				fixed[id] = true
			}
		}
	}
}

// fixedMethod reports whether one definition is a method of the inventory that an
// outside interface method of its name matches in signature, stated as the
// method's package sees it ([facts.Readings.In]). Receivers are not compared,
// because [types.Identical] ignores a signature's receiver.
func fixedMethod(one *Configured, object types.Object) (inventory.SymbolID, bool) {
	fn, isFunc := object.(*types.Func)
	if !isFunc || fn.Signature().Recv() == nil {
		return "", false
	}
	for _, method := range one.Facts.OutsideMethods(fn.Name()) {
		if want, stated := one.Facts.Readings().In(method.Method.Signature(), fn.Pkg()); stated && types.Identical(fn.Signature(), want) {
			return one.Resolve.Object(fn)
		}
	}
	return "", false
}
