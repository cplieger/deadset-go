package exempt

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/cplieger/deadset-go/internal/graph"
)

// The packages whose functions format a value with the formatting machinery, and
// the two methods that machinery reaches through an interface when a verb asks for
// a string.
const (
	formatPackage  = "fmt"
	logPackage     = "log"
	testingPackage = "testing"
	slogPackage    = "log/slog"
	stringMethod   = "String"
	errorMethod    = "Error"
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

// formatFunctions are the print, format and error-construction functions of the
// formatting package, each with the position of its format string and of its first
// operand. The scanning functions read values rather than format them, and the
// remaining function of the package formats a verb rather than an operand, so
// neither is here.
var formatFunctions = map[string]printSignature{
	"Append":   {format: verbless, operands: 1},
	"Appendf":  {format: 1, operands: 2},
	"Appendln": {format: verbless, operands: 1},
	"Errorf":   {format: 0, operands: 1, wraps: true},
	"Fprint":   {format: verbless, operands: 1},
	"Fprintf":  {format: 1, operands: 2},
	"Fprintln": {format: verbless, operands: 1},
	"Print":    {format: verbless, operands: 0},
	"Printf":   {format: 0, operands: 1},
	"Println":  {format: verbless, operands: 0},
	"Sprint":   {format: verbless, operands: 0},
	"Sprintf":  {format: 0, operands: 1},
	"Sprintln": {format: verbless, operands: 0},
}

// logFunctions are the print, fatal and panic functions of the logging package,
// which are also the methods of its logger: a method call carries no receiver
// among its arguments, so one entry answers both forms.
var logFunctions = map[string]printSignature{
	"Fatal":   {format: verbless, operands: 0},
	"Fatalf":  {format: 0, operands: 1},
	"Fatalln": {format: verbless, operands: 0},
	"Panic":   {format: verbless, operands: 0},
	"Panicf":  {format: 0, operands: 1},
	"Panicln": {format: verbless, operands: 0},
	"Print":   {format: verbless, operands: 0},
	"Printf":  {format: 0, operands: 1},
	"Println": {format: verbless, operands: 0},
}

// testingFunctions are the log, error, fatal and skip methods a test reports
// through. The methods are declared once for every testing type, on the type they
// all embed and on the interface over them, so a call on a test, a benchmark or a
// fuzz target names one of two objects of this package and both carry the name this
// table is keyed by.
var testingFunctions = map[string]printSignature{
	"Error":  {format: verbless, operands: 0},
	"Errorf": {format: 0, operands: 1},
	"Fatal":  {format: verbless, operands: 0},
	"Fatalf": {format: 0, operands: 1},
	"Log":    {format: verbless, operands: 0},
	"Logf":   {format: 0, operands: 1},
	"Skip":   {format: verbless, operands: 0},
	"Skipf":  {format: 0, operands: 1},
}

// slogFunctions are the logging functions of the structured-logging package, the
// methods of its logger, which carry the same names and the same operand positions,
// and the attribute constructors whose value is typed as any. There is no format
// string: every argument after the message, the level and the context is an
// operand, and the handler may render it with the formatting machinery.
//
// It is the one declaration of that set, because a value reaching one of these
// calls reaches this class and the encoding class both: the encoding class reads
// this table to know its own destinations.
var slogFunctions = map[string]printSignature{
	"Any":          {format: verbless, operands: 1},
	"Debug":        {format: verbless, operands: 1},
	"DebugContext": {format: verbless, operands: 2},
	"Error":        {format: verbless, operands: 1},
	"ErrorContext": {format: verbless, operands: 2},
	"Group":        {format: verbless, operands: 1},
	"Info":         {format: verbless, operands: 1},
	"InfoContext":  {format: verbless, operands: 2},
	"Log":          {format: verbless, operands: 3},
	"LogAttrs":     {format: verbless, operands: 3},
	"Warn":         {format: verbless, operands: 1},
	"WarnContext":  {format: verbless, operands: 2},
}

// printSignature is where one formatting function carries its format string and
// its operands, and whether it wraps an error.
type printSignature struct {
	format   int
	operands int
	wraps    bool
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
	var table map[string]printSignature
	switch pkg.Path() {
	case formatPackage:
		table = formatFunctions
	case logPackage:
		table = logFunctions
	case testingPackage:
		table = testingFunctions
	case slogPackage:
		table = slogFunctions
	default:
		return printSignature{}, false
	}
	sig, formats := table[fn.Name()]
	return sig, formats
}

// formatScan is what one format string says about the operands of its call: the
// operands a verb asks for a string, how far into the operand list the verbs
// reached, and whether the format selected an operand by an explicit index.
type formatScan struct {
	bound     []operand
	reached   int
	reordered bool
}

// operand is one argument a formatting call asks for a string, with the clause the
// exemption records about the verb that asked.
type operand struct {
	under string
	at    int
}

// FormatVerbContractDetector records the format-verb-contract class: a type whose
// values reach a facility that formats its operands with the formatting machinery
// keeps its String and Error methods, because the facility calls them through an
// interface and no static reference exists.
//
// The facilities are the print, format and error-construction functions of the
// formatting package, the print, fatal and panic functions of the logging package
// and the same methods of its logger, the log, error, fatal and skip methods of a
// testing type, the logging functions of the structured-logging package with the
// methods of its logger and its attribute constructors, and the functions of the
// analysed program that forward their own variadic operands to any of those.
//
// Three rules the class applies where the case is not spelled out. A format string
// the call computes rather than writes is read as binding every operand, which
// retains more than the call can reach and never less. A method is retained only
// in the form a verb calls, taking no argument and returning one string, so a
// method of another shape that shares the name is not. And an operand reaches
// through a pointer, a slice, an array and a map, because the machinery formats
// the elements of a value it is given and asks each of them for its string in
// turn; the reach stops at each defined type, so the members of a type a retained
// type is built from are not retained.
func FormatVerbContractDetector(in *Input) ([]graph.Exemption, error) {
	sites := newDeclarationSites(in.Result.Fset)
	f := &formatFlow{kept: newRetention(in, FormatVerbContract), sites: sites, wrappers: forwardingWrappers(in, sites)}
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
	for _, p := range sortedPackages(f.kept.in.Result.Packages) {
		if p.TypesInfo == nil {
			continue
		}
		for _, file := range p.Syntax {
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
		fn, isFunc := resolveObject(info, call.Fun).(*types.Func)
		if !isFunc {
			return true
		}
		if sig, formats := f.printFunction(fn); formats {
			failed = f.call(info, call, fn, sig)
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
// their operands by passing them on: a function whose final parameter is a variadic
// list of any, and whose body hands that whole list to a function that formats its
// operands, formats its own. Such a function is then itself something a further
// function can forward to, so the set grows until no function joins it, which is
// what makes a wrapper of a wrapper carry the same rule as the one it calls.
//
// A wrapper's callers write the format string when the wrapper passes a parameter
// of its own to the format position of the call it forwards to; otherwise the
// wrapper formats every operand as the verb-less forms do.
func forwardingWrappers(in *Input, sites *declarationSites) printWrappers {
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
		if inner.format >= 0 && inner.format < len(call.fromParam) {
			format = call.fromParam[inner.format]
		}
		return printSignature{format: format, operands: w.operands, wraps: inner.wraps}, true
	}
	return printSignature{}, false
}

// variadicWrappers returns every function of the analysed program whose final
// parameter is a variadic list of any, with the calls that pass that list on. It
// reads the program's own declarations through the one enumeration the boundary
// holds, so a loaded consumer's print wrapper is found exactly as the target's is.
func variadicWrappers(in *Input, sites *declarationSites) []variadicWrapper {
	var found []variadicWrapper
	for _, declared := range programFunctions(in) {
		if declared.decl.Body == nil {
			continue
		}
		if w, forwards := variadicOperands(sites, declared); forwards {
			found = append(found, w)
		}
	}
	return found
}

// variadicOperands reports whether one declaration takes a variadic list of any as
// its final parameter and hands that whole list to another function, and returns
// the calls that do. A call that passes the list element by element, or passes a
// list of its own, is not one: what the rule reads is the operands of the
// declaration's own caller reaching a formatting facility unchanged.
//
// The final parameter's type is read through the boundary's own test for a parameter
// that keeps nothing of the value it is given, which is where this package decides
// what the empty interface is.
func variadicOperands(sites *declarationSites, declared programFunction) (variadicWrapper, bool) {
	sig := declared.fn.Signature()
	params := sig.Params()
	if !sig.Variadic() || params.Len() == 0 {
		return variadicWrapper{}, false
	}
	last := params.Len() - 1
	if !slices.Contains(erasedParameters(sig), last) {
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
		if to, isFunc := resolveObject(info, call.Fun).(*types.Func); isFunc {
			w.forwards = append(w.forwards, forwardedCall{to: to, fromParam: parametersNamed(info, call.Args, strung)})
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
	return nil
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
		if bindsAString(d.verb, d.sharp, verbs) && d.arg < count {
			scan.bound = append(scan.bound, operand{at: d.arg, under: " under %" + string(d.verb)})
		}
		arg = d.arg + 1
		scan.reached = arg
	}
	return scan
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
	for _, named := range namedTypesReached(t) {
		for m := range named.Origin().Methods() {
			if rendersAString(m) {
				f.kept.record(m, site, detail)
			}
		}
	}
	return nil
}

// rendersAString reports whether m is one of the two methods a verb calls, in the
// form it calls: named String or Error, taking nothing and returning one string.
func rendersAString(m *types.Func) bool {
	if m.Name() != stringMethod && m.Name() != errorMethod {
		return false
	}
	sig := m.Signature()
	if sig.Params().Len() != 0 || sig.Results().Len() != 1 {
		return false
	}
	return types.Identical(sig.Results().At(0).Type(), types.Typ[types.String])
}
