package exempt

import (
	"go/token"
	"go/types"
	"slices"

	"github.com/cplieger/deadset-go/internal/graph"
)

// ErrorsDuckTypingDetector retains, on every type a value of which reaches a
// position typed as error, the methods the standard error helpers reach by duck
// typing: a method whose name and signature match one of Is(error) bool,
// As(any) bool, Unwrap() error and Unwrap() []error. The helpers call through no
// declared interface, so such a method carries no reference for the graph to
// hold, and the conversion set is the only evidence the program gives that the
// helpers can ever be handed a value of the type.
//
// The signature decides as much as the name. A method named Is whose parameter is
// not error implements the program's own comparison rather than the helper's
// contract, errors.Is will never call it, and it is not retained.
//
// Three cases the mechanism leaves open are decided here, each as the rule it is.
// The interface reached must be error itself rather than an interface that embeds
// error, because a wider reading retains the four forms on a type the program only
// ever uses through an interface of its own. A type converted at several sites
// retains each method once, at the first site by rendered position, because the
// exemption states why the method is live and one reason is the whole answer; the
// rendered position is what orders it, since a token.Pos orders two files by the
// order the load happened to parse them in.
func ErrorsDuckTypingDetector(in *Input) ([]graph.Exemption, error) {
	scan := &errorsDuckScan{in: in, held: make(map[graph.SymbolID]struct{})}
	scan.build()

	conversions, err := Conversions(in)
	if err != nil {
		return nil, err
	}
	sites, err := scan.sites(conversions)
	if err != nil {
		return nil, err
	}
	for i := range sites {
		if err := scan.conversion(&sites[i]); err != nil {
			return nil, err
		}
	}

	slices.SortFunc(scan.found, func(a, b errorsDuckRetained) int { return bySite(a.at, b.at) })
	found := make([]graph.Exemption, 0, len(scan.found))
	for i := range scan.found {
		found = append(found, scan.found[i].exemption)
	}
	return found, nil
}

// errorsDuckScan accumulates the methods one configuration retains.
type errorsDuckScan struct {
	in         *Input
	held       map[graph.SymbolID]struct{}
	errorIface *types.Interface
	forms      []errorsDuckForm
	found      []errorsDuckRetained
}

// errorsDuckForm is one signature the standard error helpers call, the name a
// method must carry to be that form, and the clause an exemption records.
type errorsDuckForm struct {
	signature *types.Signature
	name      string
	detail    string
}

// errorsDuckRetained is one retained method and the rendered position of its own
// declaration, which is what orders the answer.
type errorsDuckRetained struct {
	exemption graph.Exemption
	at        token.Position
}

// errorsDuckSite is one conversion to error, with its site rendered.
type errorsDuckSite struct {
	from types.Type
	at   token.Position
}

// build assembles the four forms the helpers call and the interface the
// conversion set is read for. A type may declare Unwrap under either of its two
// forms, never both.
func (s *errorsDuckScan) build() {
	errorType := types.Universe.Lookup("error").Type()
	anyType := types.Universe.Lookup("any").Type()
	boolType := types.Typ[types.Bool]

	s.errorIface, _ = errorType.Underlying().(*types.Interface)
	s.forms = []errorsDuckForm{
		{
			name: "Is", detail: "implements Is(error) bool",
			signature: s.signature([]types.Type{errorType}, []types.Type{boolType}),
		},
		{
			name: "As", detail: "implements As(any) bool",
			signature: s.signature([]types.Type{anyType}, []types.Type{boolType}),
		},
		{
			name: "Unwrap", detail: "implements Unwrap() error",
			signature: s.signature(nil, []types.Type{errorType}),
		},
		{
			name: "Unwrap", detail: "implements Unwrap() []error",
			signature: s.signature(nil, []types.Type{types.NewSlice(errorType)}),
		},
	}
}

// signature builds one form's signature from its parameter and result types.
func (s *errorsDuckScan) signature(params, results []types.Type) *types.Signature {
	return types.NewSignatureType(nil, nil, nil, s.tuple(params), s.tuple(results), false)
}

// tuple builds the unnamed tuple of one signature's parameters or results.
func (s *errorsDuckScan) tuple(list []types.Type) *types.Tuple {
	vars := make([]*types.Var, 0, len(list))
	for _, t := range list {
		vars = append(vars, types.NewVar(token.NoPos, nil, "", t))
	}
	return types.NewTuple(vars...)
}

// sites keeps the conversions whose interface is error, each with its position
// rendered, ordered so that the first site of a type is the same site whatever
// order the load parsed the files in.
func (s *errorsDuckScan) sites(conversions []Conversion) ([]errorsDuckSite, error) {
	sites := make([]errorsDuckSite, 0, len(conversions))
	for i := range conversions {
		c := &conversions[i]
		if c.To == nil || !types.Identical(c.To, s.errorIface) {
			continue
		}
		at, err := s.site(c.Site)
		if err != nil {
			return nil, err
		}
		sites = append(sites, errorsDuckSite{from: c.From, at: at})
	}
	slices.SortFunc(sites, func(a, b errorsDuckSite) int { return bySite(a.at, b.at) })
	return sites, nil
}

// conversion retains the matching methods of one conversion's concrete type.
func (s *errorsDuckScan) conversion(c *errorsDuckSite) error {
	named := s.named(c.from)
	if named == nil {
		return nil
	}
	for method := range named.Methods() {
		detail, matched := s.form(method)
		if !matched {
			continue
		}
		id, held := s.in.Resolve.Object(method)
		if !held {
			continue
		}
		if _, already := s.held[id]; already {
			continue
		}
		at, err := s.site(method.Pos())
		if err != nil {
			return err
		}
		s.held[id] = struct{}{}
		s.found = append(s.found, errorsDuckRetained{
			at: at,
			exemption: graph.Exemption{
				ID:     id,
				Class:  string(ErrorsDuckTyping),
				Site:   c.at,
				Detail: detail,
			},
		})
	}
	return nil
}

// form returns the clause of the helper form one method matches. The name
// decides first, then the signature: types.Identical compares parameters and
// results and ignores the receiver, which is what makes one set of four
// signatures serve every type.
func (s *errorsDuckScan) form(method *types.Func) (string, bool) {
	for _, f := range s.forms {
		if method.Name() == f.name && types.Identical(method.Signature(), f.signature) {
			return f.detail, true
		}
	}
	return "", false
}

// named returns the defined type a conversion's concrete type names, through a
// pointer and through an instantiation, and nil when the type is not a defined
// one. A generic type's origin is what carries the methods a file declares.
func (s *errorsDuckScan) named(t types.Type) *types.Named {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil
	}
	if origin := named.Origin(); origin != nil {
		return origin
	}
	return named
}

// site renders one position. Every position the load compiled is a position of
// the target, so one the resolver cannot render is a failure of the run rather
// than a conversion to pass over.
func (s *errorsDuckScan) site(pos token.Pos) (token.Position, error) {
	return s.in.Resolve.Render(pos)
}
