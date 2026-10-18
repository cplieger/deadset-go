package facts

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/resolve"
)

// Interface is one interface the target declares, as one configuration type-checks
// it: the methods of its method set the inventory holds, whether that method set
// fixes its implementors, and the concrete types of the target that implement it.
// A generic type is no interface and implements none here, because whether an
// uninstantiated generic type implements an interface is not a question the type
// checker answers.
type Interface struct {
	ID              inventory.SymbolID
	Methods         []inventory.SymbolID
	Implementations []Implementation

	// Sealed reports an exported interface whose method set holds an unexported
	// method: it exists to fix the types that implement it outside its package
	// rather than to be called through.
	Sealed bool
}

// Implementation is one concrete type of the target that implements an interface,
// itself or through a pointer to it, with, per method of the interface's Methods in
// that order, the declaration implementing it, empty where the target does not
// declare it, and whether that declaration's body holds no statement.
type Implementation struct {
	Concrete     inventory.SymbolID
	Implementors []inventory.SymbolID
	Empty        []bool
}

// Interfaces is every interface the target declares, by identifier, read from the
// first variant of the walk order that type-checks each.
func (x *Index) Interfaces() []Interface { return x.interfaces }

// declaredType is one named type of the target, as one configuration type-checks
// it, with its interface where it is one.
type declaredType struct {
	named *types.Named
	iface *types.Interface
	id    inventory.SymbolID
}

// interfacesOf reads the interfaces of one configuration and the concrete types
// implementing each.
func interfacesOf(x *Index, rs *resolve.Resolver) []Interface {
	ifaces, concrete := declaredTypes(x, rs)
	written := writtenFuncs(x)
	found := make([]Interface, 0, len(ifaces))
	for _, declared := range ifaces {
		found = append(found, implementedBy(rs, declared, concrete, written))
	}
	return found
}

// declaredTypes is every non-generic named type the target defines, interfaces
// apart from the rest, each by identifier and read once.
func declaredTypes(x *Index, rs *resolve.Resolver) (ifaces, concrete []declaredType) {
	seen := make(map[inventory.SymbolID]bool)
	for p := range x.Typed() {
		for _, object := range p.TypesInfo.Defs {
			name, isType := object.(*types.TypeName)
			if !isType {
				continue
			}
			id, held := rs.Object(name)
			named, isNamed := types.Unalias(name.Type()).(*types.Named)
			if !held || seen[id] || !isNamed || named.TypeParams().Len() > 0 {
				continue
			}
			seen[id] = true
			if iface, isInterface := named.Underlying().(*types.Interface); isInterface {
				ifaces = append(ifaces, declaredType{named: named, iface: iface, id: id})
			} else {
				concrete = append(concrete, declaredType{named: named, id: id})
			}
		}
	}
	byID := func(a, b declaredType) int { return strings.Compare(string(a.id), string(b.id)) }
	slices.SortFunc(ifaces, byID)
	slices.SortFunc(concrete, byID)
	return ifaces, concrete
}

// writtenFuncs is every function declaration of the target by its name's position.
func writtenFuncs(x *Index) map[token.Pos]*ast.FuncDecl {
	written := make(map[token.Pos]*ast.FuncDecl)
	for p := range x.Typed() {
		for _, f := range x.Files(p) {
			for _, decl := range f.Decls {
				if fn, isFunc := decl.(*ast.FuncDecl); isFunc {
					written[fn.Name.Pos()] = fn
				}
			}
		}
	}
	return written
}

// implementedBy is one interface with every concrete type implementing it.
func implementedBy(rs *resolve.Resolver, declared declaredType, concrete []declaredType, written map[token.Pos]*ast.FuncDecl) Interface {
	iface := declared.iface
	one := Interface{ID: declared.id, Sealed: declared.named.Obj().Exported() && restrictsImplementors(iface)}
	var methods []*types.Func
	for method := range iface.Methods() {
		if id, held := rs.Object(method); held {
			one.Methods = append(one.Methods, id)
			methods = append(methods, method)
		}
	}
	for _, t := range concrete {
		recv, satisfies := implementing(t.named, iface)
		if !satisfies {
			continue
		}
		set := types.NewMethodSet(recv)
		impl := Implementation{Concrete: t.id, Implementors: make([]inventory.SymbolID, len(methods)), Empty: make([]bool, len(methods))}
		for i, method := range methods {
			sel := set.Lookup(method.Pkg(), method.Name())
			if sel == nil {
				continue
			}
			decl, held := written[sel.Obj().Pos()]
			impl.Empty[i] = held && decl.Body != nil && len(decl.Body.List) == 0
			impl.Implementors[i], _ = rs.Object(sel.Obj())
		}
		one.Implementations = append(one.Implementations, impl)
	}
	return one
}

// restrictsImplementors reports whether one interface's method set holds an
// unexported method.
func restrictsImplementors(iface *types.Interface) bool {
	for method := range iface.Methods() {
		if !method.Exported() {
			return true
		}
	}
	return false
}

// implementing returns the form of one type that satisfies one interface, the
// pointer to it included, and reports whether either does. An interface with no
// method is satisfied by every type, so naming those types says nothing about it
// and none is recorded.
func implementing(t types.Type, iface *types.Interface) (types.Type, bool) {
	if iface.NumMethods() == 0 {
		return nil, false
	}
	if types.Implements(t, iface) {
		return t, true
	}
	if pointer := types.NewPointer(t); types.Implements(pointer, iface) {
		return pointer, true
	}
	return nil, false
}

// InterfaceMethod is what every configuration of a matrix holds about one method an
// interface of the target declares: the declarations implementing it, and whether
// it is fixed whatever calls it.
type InterfaceMethod struct {
	Implementors []inventory.SymbolID // in the order the configurations first name them

	implementations, empty int
	sealed                 bool
}

// Implemented reports whether some concrete type of the target implements the
// method.
func (m *InterfaceMethod) Implemented() bool { return m.implementations > 0 }

// Fixed reports whether the method is held whatever calls it: its interface is
// sealed, or it is a marker, every implementation of which has a body with no
// statement. A method the target does not declare has no body to read and is no
// empty implementation.
func (m *InterfaceMethod) Fixed() bool {
	return m.sealed || (m.implementations > 0 && m.empty == m.implementations)
}

// InterfaceMethods combines the interfaces of every configuration of a matrix, per's
// order being the configuration index, by method.
func InterfaceMethods(per [][]Interface) map[inventory.SymbolID]*InterfaceMethod {
	methods := make(map[inventory.SymbolID]*InterfaceMethod)
	for _, ifaces := range per {
		for _, iface := range ifaces {
			for i, id := range iface.Methods {
				m := methods[id]
				if m == nil {
					m = &InterfaceMethod{}
					methods[id] = m
				}
				m.add(&iface, i)
			}
		}
	}
	return methods
}

// add counts what one configuration's interface holds about its i-th method.
func (m *InterfaceMethod) add(iface *Interface, i int) {
	m.sealed = m.sealed || iface.Sealed
	for _, impl := range iface.Implementations {
		m.implementations++
		if impl.Empty[i] {
			m.empty++
		}
		if implementor := impl.Implementors[i]; implementor != "" && !slices.Contains(m.Implementors, implementor) {
			m.Implementors = append(m.Implementors, implementor)
		}
	}
}
