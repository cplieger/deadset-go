package exempt

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/cplieger/deadset-go/v2/internal/resolve"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// The packages whose functions format a value with the formatting machinery, and
// the methods that machinery reaches through an interface: the two a verb asking for
// a string calls, the one it calls under every verb, and the one the default verb
// with the sharp flag calls (handleMethods in go1.27.3 src/fmt/print.go).
const (
	stringMethod   = "String"
	errorMethod    = "Error"
	formatMethod   = "Format"
	goStringMethod = "GoString"
)

// stringVerbs are the verbs a string operand is valid under, and so the verbs
// under which a formatted value is asked for its string or its error. The
// wrapping verb asks an operand that is an error for its error as well, and only
// the error-construction function answers it.
const (
	stringVerbs = "vsqxX"
	wrapVerb    = "w"
)

// formatFlags are the characters that may stand between a verb's per cent sign
// and its width.
const formatFlags = "+-# 0"

// verbless is the format position of a form that carries no format string, whose
// every operand is formatted as if under the default verb.
const verbless = -1

// printSignature is where one formatting function carries its format string and
// its operands, and whether it wraps an error.
type printSignature struct {
	format   int
	operands int
	wraps    bool
}

// shifted is the signature read at the arguments of a call whose first by
// arguments supply no parameter ([facts.Callee.Shift]).
func (s printSignature) shifted(by int) printSignature {
	if s.format != verbless {
		s.format += by
	}
	s.operands += by
	return s
}

// declaredPrintFunction returns where fn carries its format string and its
// operands, and reports whether fn is one of the functions the standard library
// declares to format its operands. A method's receiver is no argument of the call,
// so a package's functions and the methods of its logger share one entry.
func declaredPrintFunction(fn *types.Func) (printSignature, bool) {
	pkg := fn.Pkg()
	if pkg == nil {
		return printSignature{}, false
	}
	sig, formats := destinations[pkg.Path()].prints[fn.Name()]
	return sig, formats
}

// formatScan is what one format string says about the operands of its call: the
// operands a verb asks for a string, the operands a verb prints, how far into the
// operand list the verbs reached, and whether the format selected an operand by an
// explicit index.
type formatScan struct {
	bound     []operand
	printed   []operand
	reached   int
	reordered bool
}

// operand is one argument a formatting call asks for a string, or prints, with the
// clause the exemption records about the verb that asked. A printed operand carries
// the verb that prints it, anyVerb where the call may print it under any, and
// whether that verb can be the default verb carrying the sharp flag, the one verb
// that calls GoString.
type operand struct {
	under  string
	at     int
	verb   rune
	sharpV bool
}

// anyVerb is the verb of an operand a call may print under any verb.
const anyVerb rune = 0

// FormatVerbContractDetector records the format-verb-contract class: a type whose
// values reach a facility that formats its operands keeps its String and Error
// methods, because the facility calls them through an interface. The facilities are
// the tables of this file and the program's functions forwarding their variadic
// operands to one. A computed format string binds every operand; only a method taking
// no argument and returning one string is retained; and an operand reaches through a
// pointer, a slice, an array and a map, stopping at each defined type.
func FormatVerbContractDetector(in *Program) ([]Exemption, error) {
	sites := newDeclarationSites(in.Result.Fset)
	f := &formatFlow{kept: newRetention(in, vocab.FormatVerbContract), sites: sites, wrappers: forwardingWrappers(in, sites)}
	if err := f.walkCalls(); err != nil {
		return nil, err
	}
	return f.kept.exemptions(), nil
}

// formatFlow accumulates the class over one loaded configuration.
type formatFlow struct {
	kept     *retention
	sites    *declarationSites
	wrappers printWrappers
}

// walkCalls records every value a formatting call asks for a string.
func (f *formatFlow) walkCalls() error {
	x := f.kept.in.Facts
	for _, p := range x.Packages() {
		for _, file := range x.Files(p) {
			if err := f.walkFile(p.TypesInfo, file); err != nil {
				return err
			}
		}
	}
	return nil
}

// walkFile records the formatting calls of one source file.
func (f *formatFlow) walkFile(info *types.Info, file *ast.File) error {
	var failed error
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || failed != nil {
			return failed == nil
		}
		callees, _ := f.kept.in.callees(info, call)
		for _, callee := range callees {
			if sig, formats := f.printFunction(callee.Fn); formats && failed == nil {
				failed = f.call(info, call, callee.Fn, sig.shifted(callee.Shift))
			}
		}
		return failed == nil
	})
	return failed
}

// printFunction returns where fn carries its format string and its operands, and
// reports whether fn formats its operands at all: either the standard library
// declares it to, or the analysed program forwards its operands to one that does.
func (f *formatFlow) printFunction(fn *types.Func) (printSignature, bool) {
	if sig, formats := declaredPrintFunction(fn); formats {
		return sig, true
	}
	sig, forwards := f.wrappers[f.sites.of(fn)]
	return sig, forwards
}

// printWrappers are the functions of the analysed program that format their
// operands by forwarding them, each kept under the position its declaration is
// written at.
type printWrappers map[token.Position]printSignature

// forwardingWrappers returns the functions of the loaded configuration that format
// their operands by handing their whole final variadic list of any to a function that
// formats its operands, grown until no function joins, so a wrapper of a wrapper
// carries the rule of the one it calls. A wrapper's callers write the format string
// when the wrapper passes its own parameter to the format position; otherwise the
// wrapper formats every operand as the verb-less forms do.
func forwardingWrappers(in *Program, sites *declarationSites) printWrappers {
	candidates := variadicWrappers(in, sites)
	found := make(printWrappers, len(candidates))
	for joined := true; joined; {
		joined = false
		for i := range candidates {
			if _, held := found[candidates[i].at]; held {
				continue
			}
			sig, forwards := candidates[i].signature(sites, found)
			if !forwards {
				continue
			}
			found[candidates[i].at] = sig
			joined = true
		}
	}
	return found
}

// variadicWrapper is one function of the analysed program that may format its
// operands by forwarding them: where its own operands start, and every call that
// hands them on.
type variadicWrapper struct {
	forwards []forwardedCall
	at       token.Position
	operands int
}

// forwardedCall is one call that passes a function's whole variadic operand list to
// another function, with the parameter of the forwarding function each argument
// names, so that a format string the forwarding function was given is recognised
// where the call puts it.
type forwardedCall struct {
	to        *types.Func
	fromParam []int
	shift     int // [facts.Callee.Shift]
}

// signature returns where the wrapper carries its format string and its operands,
// and reports whether any call it makes formats them. The first call that does
// decides, in the order the body writes them.
func (w *variadicWrapper) signature(sites *declarationSites, found printWrappers) (printSignature, bool) {
	for _, call := range w.forwards {
		inner, formats := declaredPrintFunction(call.to)
		if !formats {
			inner, formats = found[sites.of(call.to)]
		}
		if !formats {
			continue
		}
		format := verbless
		if at := inner.shifted(call.shift).format; at >= 0 && at < len(call.fromParam) {
			format = call.fromParam[at]
		}
		return printSignature{format: format, operands: w.operands, wraps: inner.wraps}, true
	}
	return printSignature{}, false
}

// variadicWrappers returns every function of the analysed program whose final
// parameter is a variadic list of any, with the calls that pass that list on. It
// reads the program's own declarations through the one enumeration [Program]
// holds, so a loaded consumer's print wrapper is found exactly as the target's is.
func variadicWrappers(in *Program, sites *declarationSites) []variadicWrapper {
	var found []variadicWrapper
	for _, declared := range in.functions {
		if declared.decl.Body == nil {
			continue
		}
		if w, forwards := variadicOperands(in, sites, declared); forwards {
			found = append(found, w)
		}
	}
	return found
}

// variadicOperands reports whether one declaration takes a variadic list of any as
// its final parameter and hands that whole list to another function, and returns the
// calls that do. A list passed element by element, or a list of its own, is not one.
// The final parameter's element type is read through the boundary's own test of the
// empty interface.
func variadicOperands(in *Program, sites *declarationSites, declared programFunction) (variadicWrapper, bool) {
	sig := declared.fn.Signature()
	params := sig.Params()
	if !sig.Variadic() || params.Len() == 0 {
		return variadicWrapper{}, false
	}
	last := params.Len() - 1
	if list, isSlice := types.Unalias(params.At(last).Type()).(*types.Slice); !isSlice || !isEmptyInterface(list.Elem()) {
		return variadicWrapper{}, false
	}

	w := variadicWrapper{at: sites.of(declared.fn), operands: last}
	operands := params.At(last)
	strung := stringParameters(params)
	info := declared.info
	ast.Inspect(declared.decl.Body, func(n ast.Node) bool {
		call, isCall := n.(*ast.CallExpr)
		if !isCall || !forwardsOperands(info, call, operands) {
			return true
		}
		callees, _ := in.callees(info, call)
		for _, to := range callees {
			w.forwards = append(w.forwards, forwardedCall{to: to.Fn, shift: to.Shift, fromParam: parametersNamed(info, call.Args, strung)})
		}
		return true
	})
	return w, len(w.forwards) > 0
}

// stringParameters indexes the parameters of one signature that are typed as a
// string by their position, which is where a format string a caller writes arrives.
func stringParameters(params *types.Tuple) map[*types.Var]int {
	strung := make(map[*types.Var]int, params.Len())
	for i := range params.Len() {
		p := params.At(i)
		if types.Identical(p.Type(), types.Typ[types.String]) {
			strung[p] = i
		}
	}
	return strung
}

// forwardsOperands reports whether one call passes operands, and nothing else, as
// the list its final argument spreads.
func forwardsOperands(info *types.Info, call *ast.CallExpr, operands *types.Var) bool {
	if !call.Ellipsis.IsValid() || len(call.Args) == 0 {
		return false
	}
	spread, isIdent := ast.Unparen(call.Args[len(call.Args)-1]).(*ast.Ident)
	if !isIdent {
		return false
	}
	return info.Uses[spread] == operands
}

// parametersNamed returns, for each argument of one call, the string parameter of
// the forwarding function the argument names, or verbless where it names none. An
// argument that builds on such a parameter names it, because the verbs the caller
// wrote are in what it built.
func parametersNamed(info *types.Info, args []ast.Expr, strung map[*types.Var]int) []int {
	named := make([]int, len(args))
	for i, arg := range args {
		named[i] = verbless
		ast.Inspect(arg, func(n ast.Node) bool {
			if named[i] != verbless {
				return false
			}
			id, isIdent := n.(*ast.Ident)
			if !isIdent {
				return true
			}
			p, isVar := info.Uses[id].(*types.Var)
			if !isVar {
				return true
			}
			if at, holds := strung[p]; holds {
				named[i] = at
			}
			return true
		})
	}
	return named
}

// call records the operands of one formatting call that a string verb binds.
func (f *formatFlow) call(info *types.Info, call *ast.CallExpr, fn *types.Func, sig printSignature) error {
	if len(call.Args) <= sig.operands {
		return nil
	}
	operands := call.Args[sig.operands:]
	for _, op := range stringOperandsOf(info, call, sig, len(operands)) {
		at := info.TypeOf(operands[op.at])
		if at == nil {
			continue
		}
		if err := f.retain(at, operands[op.at].Pos(), "formatted by "+fn.FullName()+op.under); err != nil {
			return err
		}
	}
	for _, op := range printedOperandsOf(info, call, sig, len(operands)) {
		at := info.TypeOf(operands[op.at])
		if at == nil {
			continue
		}
		site, err := f.kept.site(operands[op.at].Pos())
		if err != nil {
			return err
		}
		recordPrinted(f.kept, at, &site, "formatted by "+fn.FullName()+op.under, false, op)
	}
	return nil
}

// printedOperandsOf returns the operands one call prints, each with whether a verb
// that binds it can be the default verb carrying the sharp flag. A form carrying no
// format string prints every operand under the default verb with no flag
// (doPrint in go1.27.3 src/fmt/print.go); a format string that is not a constant, or
// operands that arrive as a spread slice, may bind any operand to any verb.
func printedOperandsOf(info *types.Info, call *ast.CallExpr, sig printSignature, count int) []operand {
	if sig.format < 0 {
		return defaultOperands(everyOperand(count, ""))
	}
	if call.Ellipsis.IsValid() || sig.format >= len(call.Args) {
		return sharpOperands(everyOperand(count, " under every verb it may carry"))
	}
	format, written := constantString(info, call.Args[sig.format])
	if !written {
		return sharpOperands(everyOperand(count, " under every verb it may carry"))
	}
	return printedOperands(format, count)
}

// printedOperands is the operands a constant format string prints: each operand a
// verb other than %T binds, and every operand past the last one a verb consumed,
// which the formatting package prints under the default verb with no flag unless the
// format selected an operand by an explicit index.
func printedOperands(format string, count int) []operand {
	scan := stringOperands(format, count, stringVerbs)
	if scan.reordered {
		return scan.printed
	}
	for at := scan.reached; at < count; at++ {
		scan.printed = append(scan.printed, operand{at: at, under: " past the format's last verb", verb: 'v'})
	}
	return scan.printed
}

// defaultOperands marks every operand as one the default verb with no flag prints.
func defaultOperands(all []operand) []operand {
	for i := range all {
		all[i].verb = 'v'
	}
	return all
}

// sharpOperands marks every operand as one any verb may print, the default verb
// with the sharp flag among them.
func sharpOperands(all []operand) []operand {
	for i := range all {
		all[i].verb, all[i].sharpV = anyVerb, true
	}
	return all
}

// stringOperandsOf returns the operands one call binds to a verb valid for a
// string. A form carrying no format string binds every operand, and so does a
// call whose format string is not a constant or whose operands arrive as a spread
// slice, because what the verbs are cannot be read at the site.
//
// An operand the verbs never reach is bound as well, under the default verb: the
// formatting package renders every operand past the last one a verb consumed, and
// does so unless the format selected an operand by an explicit index.
func stringOperandsOf(info *types.Info, call *ast.CallExpr, sig printSignature, count int) []operand {
	if sig.format < 0 {
		return everyOperand(count, "")
	}
	if call.Ellipsis.IsValid() || sig.format >= len(call.Args) {
		return everyOperand(count, " under every verb it may carry")
	}
	format, written := constantString(info, call.Args[sig.format])
	if !written {
		return everyOperand(count, " under every verb it may carry")
	}

	verbs := stringVerbs
	if sig.wraps {
		verbs += wrapVerb
	}
	scan := stringOperands(format, count, verbs)
	if scan.reordered {
		return scan.bound
	}
	for at := scan.reached; at < count; at++ {
		scan.bound = append(scan.bound, operand{at: at, under: " past the format's last verb"})
	}
	return scan.bound
}

// everyOperand binds each of count operands, which is what a call whose verbs
// cannot be read at the site is taken to do.
func everyOperand(count int, under string) []operand {
	all := make([]operand, 0, count)
	for i := range count {
		all = append(all, operand{at: i, under: under})
	}
	return all
}

// stringOperands parses one format string and returns what it says about the
// operands of its call. The grammar is the formatting package's own: a per cent
// sign, flags, an explicit argument index, a width, a precision that may carry an
// index of its own, then the verb, with a doubled per cent sign standing for the
// character and an asterisk taking its width or precision from an operand of its
// own.
func stringOperands(format string, count int, verbs string) formatScan {
	var scan formatScan
	arg := 0
	for i := 0; i < len(format); {
		if format[i] != '%' {
			i++
			continue
		}
		i++
		if i < len(format) && format[i] == '%' {
			i++
			continue
		}
		d, held := readDirective(format, i, arg)
		scan.reordered = scan.reordered || d.indexed
		i = d.next
		if !held {
			break
		}
		if d.arg < count {
			scan.add(&d, verbs)
		}
		arg = d.arg + 1
		scan.reached = arg
	}
	return scan
}

// add records what one directive binding an operand of the call says of it:
// whether the verb asks it for a string, and how it prints, which every verb but
// %T does.
func (scan *formatScan) add(d *directive, verbs string) {
	if bindsAString(d.verb, d.sharp, verbs) {
		scan.bound = append(scan.bound, operand{at: d.arg, under: " under %" + string(d.verb)})
	}
	if d.verb == 'T' {
		return
	}
	op := operand{at: d.arg, under: " under %" + string(d.verb), verb: d.verb, sharpV: d.verb == 'v' && d.sharp}
	if d.verb == 'w' {
		op.verb = 'v' // the wrapping verb prints as the default one (handleMethods)
	}
	if op.sharpV {
		op.under = " under %#v"
	}
	scan.printed = append(scan.printed, op)
}

// directive is one verb of a format string: the operand it binds, where the
// format continues after it, the verb itself, whether an explicit index chose the
// operand, and whether the sharp flag stands before the verb.
type directive struct {
	arg     int
	next    int
	verb    rune
	indexed bool
	sharp   bool
}

// readDirective parses one directive, from just after its per cent sign, over the
// operands a call reached so far, and reports whether the string held a verb at
// all.
func readDirective(format string, i, arg int) (directive, bool) {
	flags := i
	i = skipWhile(format, i, formatFlags)
	d := directive{arg: arg, sharp: strings.IndexByte(format[flags:i], '#') >= 0}

	d.arg, i, d.indexed = readIndex(format, i, d.arg)
	d.arg, i = readWidth(format, i, d.arg)
	if i < len(format) && format[i] == '.' {
		var precision bool
		d.arg, i, precision = readIndex(format, i+1, d.arg)
		d.arg, i = readWidth(format, i, d.arg)
		d.indexed = d.indexed || precision
	}
	if i >= len(format) {
		d.next = i
		return d, false
	}

	verb, size := utf8.DecodeRuneInString(format[i:])
	d.verb, d.next = verb, i+size
	return d, true
}

// bindsAString reports whether one verb asks its operand for a string. The sharp
// flag before the default verb asks for a Go-syntax representation instead, which
// the formatting package renders without calling either method.
func bindsAString(verb rune, sharp bool, verbs string) bool {
	if verb == 'v' && sharp {
		return false
	}
	return strings.ContainsRune(verbs, verb)
}

// readIndex reads an explicit argument index and returns the operand it selects,
// the position after it, and whether there was one. An index of no digits, of more
// digits than any call has operands, of zero, or with no closing bracket is not an
// index, and the formatting package renders the text as it stands.
func readIndex(format string, i, arg int) (selected, next int, indexed bool) {
	if i >= len(format) || format[i] != '[' {
		return arg, i, false
	}
	j := i + 1
	n, digits := 0, 0
	for j < len(format) && format[j] >= '0' && format[j] <= '9' {
		n = n*10 + int(format[j]-'0')
		j, digits = j+1, digits+1
	}
	if digits == 0 || digits > 6 || n < 1 || j >= len(format) || format[j] != ']' {
		return arg, i, false
	}
	return n - 1, j + 1, true
}

// readWidth reads a width or a precision, which takes an operand of its own when
// it is written as an asterisk.
func readWidth(format string, i, arg int) (reached, next int) {
	if i < len(format) && format[i] == '*' {
		return arg + 1, i + 1
	}
	return arg, skipWhile(format, i, "0123456789")
}

// skipWhile returns the first position at or after i whose character is not in
// set.
func skipWhile(format string, i int, set string) int {
	for i < len(format) && strings.IndexByte(set, format[i]) >= 0 {
		i++
	}
	return i
}

// constantString returns the value of a format string the call writes as a
// constant, and reports whether the argument is one.
func constantString(info *types.Info, expr ast.Expr) (string, bool) {
	value := info.Types[expr].Value
	if value == nil {
		// An identifier naming a constant is recorded as a use of that constant
		// rather than as an expression carrying a value.
		if c, isConst := resolveObject(info, expr).(*types.Const); isConst {
			value = c.Val()
		}
	}
	if value == nil || value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(value), true
}

// retain records the string and error methods of every defined type a value of t
// carries.
func (f *formatFlow) retain(t types.Type, at token.Pos, detail string) error {
	site, err := f.kept.site(at)
	if err != nil {
		return err
	}
	for _, named := range formattedTypes(t) {
		f.keepPrinting(named, &site, detail)
	}
	return nil
}

// keepPrinting records the string and error methods of one type, a method it has
// by promotion with the embedded fields the call reads.
func (f *formatFlow) keepPrinting(named *types.Named, site *evidence, detail string) {
	keepPrintingMethods(f.kept, named, site, detail, stringMethod, errorMethod)
}

// keepPrintingMethods records each of the named methods of one type the formatting
// machinery calls, in the form it calls it, a method the type has by promotion with
// the embedded fields the call reads.
func keepPrintingMethods(kept *retention, named *types.Named, site *evidence, detail string, names ...string) {
	for m := range named.Origin().Methods() {
		if slices.Contains(names, m.Name()) && printsAs(m) {
			kept.record(m, site, detail)
		}
	}
	set := types.NewMethodSet(types.NewPointer(named))
	for _, name := range names {
		if sel := set.Lookup(nil, name); sel != nil {
			if m, isFunc := sel.Obj().(*types.Func); isFunc && printsAs(m) {
				kept.promoted(named, sel, site, detail)
			}
		}
	}
}

// printsAs reports whether m has the signature the formatting machinery calls a
// method of its name with: String, Error and GoString taking nothing and returning
// one string, Format taking a fmt.State and a rune and returning nothing.
func printsAs(m *types.Func) bool {
	sig := m.Signature()
	switch m.Name() {
	case stringMethod, errorMethod, goStringMethod:
		return sig.Params().Len() == 0 && sig.Results().Len() == 1 &&
			types.Identical(sig.Results().At(0).Type(), types.Typ[types.String])
	case formatMethod:
		if sig.Params().Len() != 2 || sig.Results().Len() != 0 {
			return false
		}
		state, isNamed := types.Unalias(sig.Params().At(0).Type()).(*types.Named)
		return isNamed && state.Obj().Pkg() != nil && state.Obj().Pkg().Path() == "fmt" && state.Obj().Name() == "State" &&
			types.Identical(sig.Params().At(1).Type(), types.Typ[types.Rune])
	default:
		return false
	}
}

// recordPrinted records what the formatting machinery reads of one operand of
// static type t it prints under one verb, where the walk enumerates it
// ([printedFields]): every field it prints, written or not, and on every type it can
// call a method on ([formattedTypes]) the Format method, which it calls under every
// verb but %T and %p, GoString where the default verb with the sharp flag may print
// the operand, and String and Error where str says the caller does not record them
// itself. An operand the walk does not enumerate records nothing here.
func recordPrinted(kept *retention, t types.Type, site *evidence, detail string, str bool, op operand) {
	fields, enumerated := printedFields(t, op.verb, op.sharpV)
	if !enumerated {
		return
	}
	for _, field := range fields {
		kept.record(field, site, detail)
	}
	var names []string
	if op.verb != 'p' {
		names = append(names, formatMethod)
	}
	if op.sharpV {
		names = append(names, goStringMethod)
	}
	if str {
		names = append(names, stringMethod, errorMethod)
	}
	for _, named := range formattedTypes(t) {
		keepPrintingMethods(kept, named, site, detail, names...)
	}
}

// printedFields returns every field fmt prints of a value of static type t under
// one verb, following printArg, handleMethods and printValue (go1.27.3
// src/fmt/print.go): a value whose method set holds a method the verb calls
// (Format, GoString under %#v, Error or String under %v %s %q %x %X) prints none
// of its fields, no method is called below an unexported field, and only the
// operand itself is dereferenced. It reports false where a printed value has an
// interface, type-parameter or reflect.Value type, whose dynamic value t does not
// give.
func printedFields(t types.Type, verb rune, sharpV bool) ([]*types.Var, bool) {
	w := printedWalk{verb: verb, sharpV: sharpV, seen: make(map[string]bool)}
	t = types.Unalias(t)
	if w.callsMethod(t) {
		return nil, true
	}
	if pointer, isPointer := t.Underlying().(*types.Pointer); isPointer {
		t = pointer.Elem()
	}
	queue := []printedValue{{t: t, methods: true}}
	for len(queue) > 0 {
		next, known := w.step(queue[0])
		if !known {
			return nil, false
		}
		queue = append(queue[1:], next...)
	}
	return w.fields, true
}

// printedValue is one value the formatting machinery prints, by its static type,
// and whether it may call a method on it.
type printedValue struct {
	t       types.Type
	methods bool
}

// printedWalk is the state of one [printedFields] walk.
type printedWalk struct {
	seen   map[string]bool
	fields []*types.Var
	verb   rune
	sharpV bool
}

// step records the fields one printed value prints and returns the values beneath
// it the machinery prints, reporting false where the value's type is one whose
// dynamic value the static type does not give.
func (w *printedWalk) step(v printedValue) ([]printedValue, bool) {
	t := types.Unalias(v.t)
	key := resolve.Spell(t, nil)
	if v.methods {
		key += "\x00methods"
	}
	if w.seen[key] {
		return nil, true
	}
	w.seen[key] = true
	if isReflectHandle(t) {
		return nil, false
	}
	if _, isInterface := t.Underlying().(*types.Interface); isInterface {
		return nil, false
	}
	if v.methods && w.callsMethod(t) {
		return nil, true
	}
	var next []printedValue
	switch u := t.Underlying().(type) {
	case *types.Struct:
		for field := range u.Fields() {
			w.fields = append(w.fields, field)
			next = append(next, printedValue{t: field.Type(), methods: v.methods && field.Exported()})
		}
	case *types.Array:
		next = append(next, printedValue{t: u.Elem(), methods: v.methods})
	case *types.Slice:
		next = append(next, printedValue{t: u.Elem(), methods: v.methods})
	case *types.Map:
		next = append(next, printedValue{t: u.Key(), methods: v.methods}, printedValue{t: u.Elem(), methods: v.methods})
	}
	return next, true
}

// callsMethod reports whether the machinery prints a value of type t through a
// method of its method set under the walk's verb rather than by its fields. Under
// any verb only Format is sure to be called.
func (w *printedWalk) callsMethod(t types.Type) bool {
	if _, isInterface := t.Underlying().(*types.Interface); isInterface || w.verb == 'p' {
		return false
	}
	set := types.NewMethodSet(t)
	has := func(name string) bool {
		sel := set.Lookup(nil, name)
		if sel == nil {
			return false
		}
		m, isFunc := sel.Obj().(*types.Func)
		return isFunc && printsAs(m)
	}
	switch {
	case has(formatMethod):
		return true
	case w.verb == anyVerb:
		return false
	case w.verb == 'v' && w.sharpV:
		return has(goStringMethod)
	case strings.ContainsRune(stringVerbs, w.verb):
		return has(errorMethod) || has(stringMethod)
	default:
		return false
	}
}

// formattedTypes returns every defined type the machinery formats a value of t
// through: the types the value is composed of, and those an embedded or exported
// field of a struct type among them holds, defined or with no name, at any depth.
// The walk stops at an interface, whose dynamic type is not seen here, and at an
// unexported field, which is printed without a method call.
func formattedTypes(t types.Type) []*types.Named {
	w := formatWalk{seen: make(map[string]bool), walked: make(map[*types.Struct]bool)}
	for queue := []types.Type{t}; len(queue) > 0; {
		next := queue[0]
		queue = append(queue[1:], w.step(next)...)
	}
	return w.found
}

// formatWalk is the state of one [formattedTypes] walk.
type formatWalk struct {
	seen   map[string]bool
	walked map[*types.Struct]bool
	found  []*types.Named
}

// step records the defined types a value of t is composed of and returns the types
// the walk continues to: the underlying type of each, and the fields of each struct.
func (w *formatWalk) step(t types.Type) []types.Type {
	named, structs := typesReached(t)
	var next []types.Type
	for _, one := range named {
		key := resolve.Spell(one, nil)
		if w.seen[key] {
			continue
		}
		w.seen[key] = true
		w.found = append(w.found, one)
		if st, isStruct := one.Underlying().(*types.Struct); isStruct {
			structs = append(structs, st)
		} else {
			next = append(next, one.Underlying())
		}
	}
	for _, st := range structs {
		next = append(next, w.fields(st)...)
	}
	return next
}

// fields returns the types of the fields of one struct the machinery formats, once
// per struct.
func (w *formatWalk) fields(st *types.Struct) []types.Type {
	if w.walked[st] {
		return nil
	}
	w.walked[st] = true
	var held []types.Type
	for field := range st.Fields() {
		if field.Exported() || field.Embedded() {
			held = append(held, field.Type())
		}
	}
	return held
}
