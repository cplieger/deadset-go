package exempt

import (
	"slices"
	"testing"
)

func TestLinknameCgoAsmPluginDetectorRetainsBothSidesOfALinknameDirective(t *testing.T) {
	in := inputOf(t, "linkname.txtar", Options{})

	got, err := LinknameCgoAsmPluginDetector(in)
	if err != nil {
		t.Fatalf("LinknameCgoAsmPluginDetector(linkname.txtar) error: %v", err)
	}

	want := []string{
		"pushed\tlinkname-cgo-asm-plugin\tapp.go:5:1\tnamed by a go:linkname directive",
		"pulled\tlinkname-cgo-asm-plugin\tapp.go:8:1\tnamed by a go:linkname directive",
		"Value\tlinkname-cgo-asm-plugin\tapp.go:8:1\tnamed by a go:linkname directive",
		"absent\tlinkname-cgo-asm-plugin\tapp.go:11:1\tnamed by a go:linkname directive",
	}
	if lines := rendered(in, got); !slices.Equal(lines, want) {
		t.Errorf("LinknameCgoAsmPluginDetector(linkname.txtar) = %q, want %q", lines, want)
	}
}

func TestLinknameCgoAsmPluginDetectorRetainsTheDeclarationATextDirectiveNames(t *testing.T) {
	in := inputOf(t, "assembly.txtar", Options{})

	got, err := LinknameCgoAsmPluginDetector(in)
	if err != nil {
		t.Fatalf("LinknameCgoAsmPluginDetector(assembly.txtar) error: %v", err)
	}

	want := []string{
		"add\tlinkname-cgo-asm-plugin\tadd_amd64.s:4:1\tnamed by an assembly TEXT directive",
		"fileLocal\tlinkname-cgo-asm-plugin\tadd_amd64.s:11:2\tnamed by an assembly TEXT directive",
	}
	if lines := rendered(in, got); !slices.Equal(lines, want) {
		t.Errorf("LinknameCgoAsmPluginDetector(assembly.txtar) = %q, want %q", lines, want)
	}
}

func TestLinknameCgoAsmPluginDetectorRetainsTheExportedSymbolsOfAPluginMainPackage(t *testing.T) {
	in := inputOf(t, "plugin.txtar", Options{})

	got, err := LinknameCgoAsmPluginDetector(in)
	if err != nil {
		t.Fatalf("LinknameCgoAsmPluginDetector(plugin.txtar) error: %v", err)
	}

	want := []string{
		"Handler\tlinkname-cgo-asm-plugin\tplug.go:1:1\texported from a plugin's main package",
		"Greet\tlinkname-cgo-asm-plugin\tplug.go:1:1\texported from a plugin's main package",
	}
	if lines := rendered(in, got); !slices.Equal(lines, want) {
		t.Errorf("LinknameCgoAsmPluginDetector(plugin.txtar) = %q, want %q", lines, want)
	}
}

// TestLinknameCgoAsmPluginDetectorRetainsNoCgoExportUnderACgoDisabledLoad pins
// what the cgo mechanism can reach today: the load compiles with cgo disabled, so
// the file carrying the export directive is not among the syntax the class walks
// and is named as a declared limit of the run instead.
func TestLinknameCgoAsmPluginDetectorRetainsNoCgoExportUnderACgoDisabledLoad(t *testing.T) {
	in := inputOf(t, "cgo-export.txtar", Options{})

	wantExcluded := []string{"export.go"}
	if !slices.Equal(in.Result.ExcludedByCgo, wantExcluded) {
		t.Fatalf("Setup: load(cgo-export.txtar).ExcludedByCgo = %q, want %q",
			in.Result.ExcludedByCgo, wantExcluded)
	}

	got, err := LinknameCgoAsmPluginDetector(in)
	if err != nil {
		t.Fatalf("LinknameCgoAsmPluginDetector(cgo-export.txtar) error: %v", err)
	}
	if lines := rendered(in, got); len(lines) != 0 {
		t.Errorf("LinknameCgoAsmPluginDetector(cgo-export.txtar) = %q, want no exemption", lines)
	}
}

func TestTextSymbol(t *testing.T) {
	tests := []struct {
		name          string
		line          string
		symbol        string
		at            int
		wantDirective bool
	}{
		{name: "plain", line: "TEXT ·add(SB), NOSPLIT, $0-24", symbol: "add", wantDirective: true},
		{name: "tab", line: "TEXT\t·add(SB)", symbol: "add", wantDirective: true},
		{
			name: "indented file-local", line: "\tTEXT ·fileLocal<>(SB), NOSPLIT, $0",
			symbol: "fileLocal", at: 1, wantDirective: true,
		},
		{name: "digits and underscore", line: "TEXT ·add_2(SB)", symbol: "add_2", wantDirective: true},
		{name: "commented", line: "// TEXT ·add(SB)"},
		{name: "another package", line: "TEXT example·add(SB)"},
		{name: "longer word", line: "TEXTUAL ·add(SB)"},
		{name: "no symbol", line: "TEXT ·(SB)"},
		{name: "no middle dot", line: "TEXT add(SB)"},
		{name: "no operand", line: "TEXT"},
		{name: "not a text directive", line: "GLOBL ·data(SB), RODATA, $8"},
		{name: "empty", line: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			symbol, at, ok := textSymbol(test.line)
			if ok != test.wantDirective {
				t.Fatalf("textSymbol(%q) ok = %t, want %t", test.line, ok, test.wantDirective)
			}
			if symbol != test.symbol {
				t.Errorf("textSymbol(%q) symbol = %q, want %q", test.line, symbol, test.symbol)
			}
			if ok && at != test.at {
				t.Errorf("textSymbol(%q) at = %d, want %d", test.line, at, test.at)
			}
		})
	}
}
