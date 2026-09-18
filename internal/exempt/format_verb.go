package exempt

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strings"
	"unicode/utf8"

	"github.com/cplieger/deadset-go/internal/graph"
)

// The package that formats a value, and the two methods it reaches through an
// interface when a verb asks for a string.
const (
	formatPackage = "fmt"
	stringMethod  = "String"
	errorMethod   = "Error"
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

// printFunctions are the print, format and error-construction functions of the
// formatting package, each with the position of its format string and of its first
// operand. The scanning functions read values rather than format them, and the
// remaining function of the package formats a verb rather than an operand, so
// neither is here.
var printFunctions = map[string]printSignature{
	"Append":   {format: -1, operands: 1},
	"Appendf":  {format: 1, operands: 2},
	"Appendln": {format: -1, operands: 1},
	"Errorf":   {format: 0, operands: 1, wraps: true},
	"Fprint":   {format: -1, operands: 1},
	"Fprintf":  {format: 1, operands: 2},
	"Fprintln": {format: -1, operands: 1},
	"Print":    {format: -1, operands: 0},
	"Printf":   {format: 0, operands: 1},
	"Println":  {format: -1, operands: 0},
	"Sprint":   {format: -1, operands: 0},
	"Sprintf":  {format: 0, operands: 1},
	"Sprintln": {format: -1, operands: 0},
}

// printSignature is where one formatting function carries its format string and
// its operands, and whether it wraps an error. A format of -1 names a form that
// carries no format string, whose every operand is formatted as if under the
// default verb.
type printSignature struct {
	format   int
	operands int
	wraps    bool
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
// values reach a formatting verb valid for a string operand keeps its String and
// Error methods, because the formatting package calls them through an interface
// and no static reference exists.
//
// Three rules the class applies where the case is not spelled out. A format string
// the call computes rather than writes is read as binding every operand, which
// retains more than the call can reach and never less. A method is retained only
// in the form a verb calls, taking no argument and returning one string, so a
// method of another shape that shares the name is not. And an operand reaches
// through a pointer, a slice, an array, a map, a channel and a type argument,
// because the formatting package formats the elements of a value it is given and
// asks each of them for its string in turn; the reach stops at each defined type,
// so the members of a type a retained type is built from are not retained.
func FormatVerbContractDetector(in *Input) ([]graph.Exemption, error) {
	f := &formatFlow{kept: newRetention(in, FormatVerbContract)}
	if err := f.walkCalls(); err != nil {
		return nil, err
	}
	return f.kept.exemptions(), nil
}

// formatFlow accumulates the class over one loaded configuration.
type formatFlow struct {
	kept *retention
}

// walkCalls records every value a formatting call asks for a string.
func (f *formatFlow) walkCalls() error {
	for _, p := range targetPackages(f.kept.in.Result.Packages) {
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
		if !isFunc || fn.Pkg() == nil || fn.Pkg().Path() != formatPackage {
			return true
		}
		if sig, formats := printFunctions[fn.Name()]; formats {
			failed = f.call(info, call, fn, sig)
		}
		return failed == nil
	})
	return failed
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
