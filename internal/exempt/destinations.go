package exempt

import (
	"go/types"
	"reflect"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/facts"
)

// destinationRow is what one standard package is to the encoding-reflection and
// format-verb rules.
type destinationRow struct {
	// refusals are the functions by which a program makes the package's decoder
	// refuse a document naming a member the type lacks.
	refusals map[string]bool
	// executions are a template engine's methods that execute a template.
	executions map[string]bool
	// finders are the methods by which a program finds a method of a value through
	// reflection, and fieldsOnly the one function that reads fields and reaches no
	// method whatever the program finds.
	finders map[string]bool
	// prints are the functions formatting their operands, each with where it
	// carries its format string and its operands.
	prints map[string]printSignature
	// scanners are the receivers whose Scan method fills only a pointer argument.
	scanners   map[string]bool
	fieldsOnly string
	// methods are the methods every function of the package resolves by name on a
	// value it is given, nonzero for a package each function of which is a
	// destination: a standard encoder resolves the methods of [methodsResolvedByName]
	// and calls no other, a template engine selects a method by the syntax it selects
	// a field with.
	methods methodReach
	// key is the struct tag key the package's encoder names a field by.
	key tagKeys
	// xmlName is whether the package's encoder and decoder read a value's XMLName
	// field, for the element's name.
	xmlName bool
	// logValues is whether each of prints also resolves LogValue and reads fields.
	logValues bool
}

// The packages a rule also reads other than through [destinations].
const (
	reflectPackage = "reflect"
	xmlPackage     = "encoding/xml"
	htmlTemplate   = "html/template"
)

var (
	unknownMemberRefusals = map[string]bool{"DisallowUnknownFields": true, "RejectUnknownMembers": true}
	templateExecutions    = map[string]bool{"Execute": true, "ExecuteTemplate": true}
)

// destinations is every standard package a destination rule names, by import path.
// The rule for a callee the analysis cannot read passes over them, so one call is
// one record. The sorting package's empty-interface parameters swap elements and
// read no field.
var destinations = map[string]destinationRow{
	"encoding/gob":     {methods: reachGobEncode | reachGobDecode},
	"encoding/json":    {methods: reachJSONEncode | reachJSONDecode, key: jsonKey, refusals: unknownMemberRefusals},
	"encoding/json/v2": {methods: reachJSONEncode | reachJSONDecode, key: jsonKey, refusals: unknownMemberRefusals},
	xmlPackage:         {methods: reachXMLEncode | reachXMLDecode, key: xmlKey, xmlName: true},
	htmlTemplate:       {methods: reachExportedMethods, executions: templateExecutions},
	"text/template":    {methods: reachExportedMethods, executions: templateExecutions},
	reflectPackage: {
		methods:    reachExportedMethods,
		finders:    map[string]bool{"Method": true, "MethodByName": true, "NumMethod": true},
		fieldsOnly: "DeepEqual",
	},
	"database/sql": {scanners: map[string]bool{"Rows": true, "Row": true}},
	"sort":         {},
	"fmt":          {prints: formatFunctions},
	"log":          {prints: logFunctions},
	"testing":      {prints: testingFunctions},
	"log/slog":     {prints: slogFunctions, logValues: true},
}

// taggedReads are, per package outside the standard library, the struct tag keys by
// which it reads a field of a value it is given whatever the program writes: go-flags
// runs the subcommand a field tagged command holds
// (https://pkg.go.dev/github.com/jessevdk/go-flags#hdr-Commands).
var taggedReads = map[string][]string{"github.com/jessevdk/go-flags": {"command"}}

// readsByTag reports whether one of the outside packages reads a field by its tag.
func readsByTag(outside []*types.Package, tag string) bool {
	for _, pkg := range outside {
		for _, key := range taggedReads[pkg.Path()] {
			if _, tagged := reflect.StructTag(tag).Lookup(key); tagged {
				return true
			}
		}
	}
	return false
}

// namedDestination reports whether a rule names the destinations of one package.
func namedDestination(path string) bool {
	_, named := destinations[path]
	return named
}

// templateEngine reports whether one package is a template engine.
func templateEngine(path string) bool {
	return destinations[path].executions != nil
}

// The two encoding methods more than one encoder resolves.
const (
	marshalJSON = "MarshalJSON"
	marshalText = "MarshalText"
)

// methodsResolvedByName are the methods a destination resolves by name on a value it
// encodes or decodes, each with the destinations that resolve it. A destination
// walking a value keeps such a method on every defined type it reaches, because the
// destination looks the method up on the type and the reference graph holds no edge
// to it.
var methodsResolvedByName = map[string]methodReach{
	"AppendText":        reachJSONEncode,
	"GobDecode":         reachGobDecode,
	"GobEncode":         reachGobEncode,
	"LogValue":          reachLogValue,
	"MarshalBinary":     reachGobEncode,
	marshalJSON:         reachJSONEncode,
	"MarshalJSONTo":     reachJSONEncode,
	marshalText:         reachJSONEncode | reachXMLEncode,
	"MarshalXML":        reachXMLEncode,
	"MarshalXMLAttr":    reachXMLEncode,
	"UnmarshalBinary":   reachGobDecode,
	"UnmarshalJSON":     reachJSONDecode,
	"UnmarshalJSONFrom": reachJSONDecode,
	"UnmarshalText":     reachJSONDecode | reachXMLDecode,
	"UnmarshalXML":      reachXMLDecode,
	"UnmarshalXMLAttr":  reachXMLDecode,
}

// selfEncoders are, per encoding direction, the methods by which a type encodes
// itself, so the encoder calls the method and walks no field of the type
// (https://pkg.go.dev/encoding/json#Marshal, https://pkg.go.dev/encoding/xml#Marshal,
// https://pkg.go.dev/encoding/gob).
var selfEncoders = map[methodReach][]selfEncoder{
	reachJSONEncode: {{marshalJSON, 0, 2}, {"MarshalJSONTo", 1, 1}, {marshalText, 0, 2}},
	reachXMLEncode:  {{"MarshalXML", 2, 1}, {marshalText, 0, 2}},
	reachGobEncode:  {{"GobEncode", 0, 2}, {"MarshalBinary", 0, 2}},
}

// The prefixes an entry point of an encoder names its direction by.
var (
	encodingEntryPoints = [...]string{"Encode", "Marshal"}
	decodingEntryPoints = [...]string{"Decode", "Unmarshal"}
)

// fillingNames are the name prefixes by which a function or method outside the
// analysed program says it fills the value it is given from a document. Such a callee
// may also read what it fills, as a validating binder does, so a field it alone
// retains is reported at the class possible rather than held live.
var fillingNames = [...]string{"Bind", "Decode", "MustBind", "ShouldBind", "Unmarshal"}

// printingMethods are the methods a template engine calls on a value it prints
// whatever its templates name, through fmt (https://pkg.go.dev/text/template#hdr-Actions).
var printingMethods = map[string]bool{errorMethod: true, "Format": true, stringMethod: true}

// entryPoint is what one function of an encoder retains of a value it is given: an
// encoding entry point reads fields and resolves the encoding methods, a decoding one
// fills fields through reflection, which reads none, and resolves the decoding
// methods, and any other, a registration among them, retains both. A JSON decoder
// retains fields as well where the program makes it refuse unknown members. Every
// entry point of the XML package reads XMLName.
func entryPoint(row *destinationRow, name string, unknownMembers bool) reach {
	switch {
	case hasAnyPrefix(name, encodingEntryPoints[:]):
		return reach{
			methods: row.methods &^ reachDecoding, fields: true, keys: row.key, encoders: row.methods & reachEncoding,
			xmlName: row.xmlName,
		}
	case decodingEntryPoint(row, name):
		return reach{
			methods: row.methods &^ reachEncoding, fields: unknownMembers && row.refusals != nil, keys: row.key,
			xmlName: row.xmlName, holders: true,
		}
	default:
		return reach{methods: row.methods, fields: true, keys: row.key, xmlName: row.xmlName}
	}
}

func decodingEntryPoint(row *destinationRow, name string) bool {
	return row.methods&reachDecoding != 0 && hasAnyPrefix(name, decodingEntryPoints[:])
}

// decodesWireValue reports whether a call to fn fills its argument from a document
// the program reads: a decoding entry point of an encoder, or the Scan of a
// database row.
func decodesWireValue(fn *types.Func) bool {
	if fn.Pkg() == nil {
		return false
	}
	row := destinations[fn.Pkg().Path()]
	return decodingEntryPoint(&row, fn.Name()) || scansARow(fn, row.scanners)
}

// hasAnyPrefix reports whether name begins with one of the prefixes.
func hasAnyPrefix(name string, prefixes []string) bool {
	return slices.ContainsFunc(prefixes, func(prefix string) bool {
		return strings.HasPrefix(name, prefix)
	})
}

// encodingDestination reports whether a call to fn reaches the class, and how.
func (f *encodingFlow) encodingDestination(fn *types.Func) (destination, bool) {
	pkg := fn.Pkg()
	if pkg == nil {
		return destination{}, false
	}
	detail := "passed to " + fn.FullName()
	if _, contains := facts.StandardContainer(fn); contains {
		return destination{detail: detail, reach: reach{contained: []*types.Package{pkg}}}, true
	}
	row, named := destinations[pkg.Path()]
	switch {
	case !named:
		return destination{}, false
	case row.finders != nil && (fn.Name() == row.fieldsOnly || !f.findsMethods):
		return destination{detail: detail, reach: reach{fields: true}}, true
	case row.methods&reachEncoding != 0:
		return destination{detail: detail, reach: entryPoint(&row, fn.Name(), f.unknownMembers)}, true
	case row.methods != 0:
		return destination{detail: detail, reach: reach{methods: row.methods, fields: true, results: row.executions[fn.Name()]}}, true
	case scansARow(fn, row.scanners):
		return destination{detail: "scanned by " + fn.FullName(), reach: reach{fields: true}, pointers: true}, true
	}
	if _, logs := row.prints[fn.Name()]; row.logValues && logs {
		return destination{detail: detail, reach: reach{methods: reachLogValue, fields: true}}, true
	}
	return destination{}, false
}

// scansARow reports whether fn is the Scan method of one of the receivers rather
// than another Scan the same package declares.
func scansARow(fn *types.Func, receivers map[string]bool) bool {
	recv := fn.Signature().Recv()
	if recv == nil || fn.Name() != "Scan" {
		return false
	}
	t := types.Unalias(recv.Type())
	if p, isPointer := t.(*types.Pointer); isPointer {
		t = types.Unalias(p.Elem())
	}
	named, isNamed := t.(*types.Named)
	return isNamed && receivers[named.Obj().Name()]
}

// namesFunction reports whether any file of the analysed program uses a function or
// method of a package of [destinations] whose name the row's names hold.
func namesFunction(in *Program, names func(destinationRow) map[string]bool) bool {
	for _, p := range in.Facts.Program() {
		if p.TypesInfo == nil {
			continue
		}
		for _, object := range p.TypesInfo.Uses {
			if _, isFunc := object.(*types.Func); isFunc && object.Pkg() != nil &&
				names(destinations[object.Pkg().Path()])[object.Name()] {
				return true
			}
		}
	}
	return false
}

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
// methods of its logger, which carry the same names and operand positions, and the
// attribute constructors whose value is typed as any. Every argument after the
// message, the level and the context is an operand.
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
