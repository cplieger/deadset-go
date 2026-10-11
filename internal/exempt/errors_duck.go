package exempt

import (
	"cmp"
	"go/token"
	"go/types"
	"slices"

	"github.com/cplieger/deadset-go/internal/graph"
)

// ErrorsDuckTypingDetector retains, on every type reachable as an error, the methods
// the error helpers call by duck typing: Is(error) bool, As(any) bool, Unwrap() error
// and Unwrap() []error, matched by name and signature. A type a site of the program
// converts to error retains each method once, at its first site by rendered position.
// A type entering another interface, from which a chain of sites leads to error,
// retains each method once per chain, held by every declaration holding a site of it.
func ErrorsDuckTypingDetector(in *Input) ([]graph.Exemption, error) {
	scan := &errorsDuckScan{in: in, held: make(map[graph.SymbolID]struct{}), chained: make(map[string]bool)}
	scan.build()

	all, err := allConversionsOf(in)
	if err != nil {
		return nil, err
	}
	all = append(slices.Clip(all), publishedConversions(in)...)
	sites, err := scan.sites(all)
	if err != nil {
		return nil, err
	}
	for i := range sites {
		if err := scan.conversion(&sites[i]); err != nil {
			return nil, err
		}
	}
	if err := scan.chains(all); err != nil {
		return nil, err
	}

	slices.SortStableFunc(scan.found, func(a, b errorsDuckRetained) int { return graph.ByPosition(a.at, b.at) })
	found := make([]graph.Exemption, 0, len(scan.found))
	for i := range scan.found {
		found = append(found, scan.found[i].exemption)
	}
	return found, nil
}

// errorsDuckScan accumulates the methods one configuration retains.
type errorsDuckScan struct {
	in         *Input
	held       map[graph.SymbolID]struct{} // retained unheld, through a site the program writes to error
	chained    map[string]bool             // retained held, by method and holders
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

// errorsDuckSite is one conversion to error, with its site rendered, and the
// declarations holding it where it is derived from sites several hold.
type errorsDuckSite struct {
	from      types.Type
	holder    graph.SymbolID
	coHolders []graph.SymbolID
	at        token.Position
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
func (*errorsDuckScan) tuple(list []types.Type) *types.Tuple {
	vars := make([]*types.Var, 0, len(list))
	for _, t := range list {
		vars = append(vars, types.NewVar(token.NoPos, nil, "", t))
	}
	return types.NewTuple(vars...)
}

// sites keeps the conversions of a concrete value whose interface is error and that
// the program writes, each with its position rendered, ordered so that the first
// site of a type is the same site whatever order the load parsed the files in. A
// site the mode holds comes first, because a method keeps only its first site and a
// test file's site retains nothing. A site an assertion derives is a chain's
// ([errorsDuckScan.chains]).
func (s *errorsDuckScan) sites(conversions []graph.Conversion) ([]errorsDuckSite, error) {
	sites := make([]errorsDuckSite, 0, len(conversions))
	for i := range conversions {
		c := &conversions[i]
		if c.To == nil || !types.Identical(c.To, s.errorIface) || types.IsInterface(c.From) || len(c.Held) > 0 {
			continue
		}
		at, err := s.site(c.Site)
		if err != nil {
			return nil, err
		}
		sites = append(sites, errorsDuckSite{from: c.From, at: at})
	}
	rank := func(at token.Position) int {
		if holdsInMode(&graph.Exemption{Site: at}, s.in.Mode) {
			return 0
		}
		return 1
	}
	slices.SortFunc(sites, func(a, b errorsDuckSite) int {
		return cmp.Or(cmp.Compare(rank(a.at), rank(b.at)), graph.ByPosition(a.at, b.at))
	})
	return sites, nil
}

// chains retains the matching methods of each concrete type a site the program
// writes, or one an assertion derives, brings to an interface from which a chain of
// sites leads to error, once per chain, held by the declarations holding the sites.
// A method a site to error retains unheld is not retained again.
func (s *errorsDuckScan) chains(all []graph.Conversion) error {
	spans := declarationSpans(s.in)
	chains := graph.NewChains(all, func(i int) string {
		holder, _ := spans.declarationAt(s.in, &all[i], all[i].Site)
		return string(holder)
	})
	for i := range all {
		c := &all[i]
		if c.To == nil || types.IsInterface(c.From) {
			continue
		}
		direct := types.Identical(c.To, s.errorIface)
		if direct && len(c.Held) == 0 {
			continue
		}
		if err := s.throughChains(spans, chains, c); err != nil {
			return err
		}
	}
	return nil
}

// throughChains retains the matching methods of one site's concrete type once per
// chain from the site's interface to error.
func (s *errorsDuckScan) throughChains(spans *spans, chains *graph.Chains, c *graph.Conversion) error {
	start := c.Held
	if len(start) == 0 {
		start = []token.Pos{c.Site}
	}
	for _, route := range chains.Routes(c.To, s.errorIface) {
		positions := append(slices.Clone(start), route...)
		holder, coHolders := spans.holdersAt(s.in, c, positions)
		at, err := s.site(s.evidence(c.Site, positions))
		if err != nil {
			return err
		}
		if err := s.conversion(&errorsDuckSite{from: c.From, holder: holder, coHolders: coHolders, at: at}); err != nil {
			return err
		}
	}
	return nil
}

// evidence is where a chain's evidence stands: the first of its sites a test file
// holds, because the chain is then a test's evidence, and its first site otherwise.
func (s *errorsDuckScan) evidence(first token.Pos, positions []token.Pos) token.Pos {
	for _, at := range positions {
		if _, test := graph.IsTestFile(s.in.Result.Fset.Position(at).Filename); test {
			return at
		}
	}
	return first
}

// conversion retains the matching methods of one conversion's concrete type: once
// per method for a site the program writes to error, and once per method and set
// of holders for a held one.
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
		if !s.first(id, c) {
			continue
		}
		at, err := s.site(method.Pos())
		if err != nil {
			return err
		}
		s.found = append(s.found, errorsDuckRetained{
			at: at,
			exemption: graph.Exemption{
				ID:        id,
				Class:     string(ErrorsDuckTyping),
				Site:      c.at,
				Detail:    detail,
				Holder:    c.holder,
				CoHolders: c.coHolders,
			},
		})
	}
	return nil
}

// first reports whether one method is retained for the first time at one site: once
// per method unheld, and once per method and set of holders held. A method retained
// unheld is never retained again.
func (s *errorsDuckScan) first(id graph.SymbolID, c *errorsDuckSite) bool {
	if _, already := s.held[id]; already {
		return false
	}
	if c.holder == "" {
		s.held[id] = struct{}{}
		return true
	}
	key := string(id) + "\x00" + string(c.holder) + "\x00" + graph.JoinedIDs(c.coHolders)
	if s.chained[key] {
		return false
	}
	s.chained[key] = true
	return true
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
func (*errorsDuckScan) named(t types.Type) *types.Named {
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
