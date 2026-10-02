package exempt

import (
	"slices"
	"testing"
)

// TestLinknameCgoAsmPluginDetectorRetainsNeitherSideOfALinknameDirective pins
// that a linkname directive is no evidence of this class: the names it joins are
// roots of the analysis, which the graph's root detection keeps.
func TestLinknameCgoAsmPluginDetectorRetainsNeitherSideOfALinknameDirective(t *testing.T) {
	shared := analysisOf(t, "linkname.txtar", Options{})
	symbols := shared.inventory(t)

	got, err := shared.detect(t, LinknameCgoAsmPlugin)
	if err != nil {
		t.Fatalf("LinknameCgoAsmPluginDetector(linkname.txtar) error: %v", err)
	}
	if lines := rendered(symbols, got); len(lines) != 0 {
		t.Errorf("LinknameCgoAsmPluginDetector(linkname.txtar) = %q, want none", lines)
	}
}

func TestLinknameCgoAsmPluginDetectorRetainsTheDeclarationATextDirectiveNames(t *testing.T) {
	shared := analysisOf(t, "assembly.txtar", Options{})
	symbols := shared.inventory(t)

	got, err := shared.detect(t, LinknameCgoAsmPlugin)
	if err != nil {
		t.Fatalf("LinknameCgoAsmPluginDetector(assembly.txtar) error: %v", err)
	}

	want := []string{
		"add\tlinkname-cgo-asm-plugin\tadd_amd64.s:4:1\tnamed by an assembly TEXT directive",
		"fileLocal\tlinkname-cgo-asm-plugin\tadd_amd64.s:11:2\tnamed by an assembly TEXT directive",
	}
	if lines := rendered(symbols, got); !slices.Equal(lines, want) {
		t.Errorf("LinknameCgoAsmPluginDetector(assembly.txtar) = %q, want %q", lines, want)
	}
}

func TestLinknameCgoAsmPluginDetectorRetainsTheExportedSymbolsOfAPluginMainPackage(t *testing.T) {
	shared := analysisOf(t, "plugin.txtar", Options{})
	symbols := shared.inventory(t)

	got, err := shared.detect(t, LinknameCgoAsmPlugin)
	if err != nil {
		t.Fatalf("LinknameCgoAsmPluginDetector(plugin.txtar) error: %v", err)
	}

	want := []string{
		"Handler\tlinkname-cgo-asm-plugin\tplug.go:1:1\texported from a plugin's main package",
		"Greet\tlinkname-cgo-asm-plugin\tplug.go:1:1\texported from a plugin's main package",
	}
	if lines := rendered(symbols, got); !slices.Equal(lines, want) {
		t.Errorf("LinknameCgoAsmPluginDetector(plugin.txtar) = %q, want %q", lines, want)
	}
}

// TestLinknameCgoAsmPluginDetectorRetainsNoCgoExport pins that an export
// directive is no evidence of this class. The load type-checks the file carrying
// it with the C pseudo-package opaque, so the file is among the syntax the class
// walks, and the function C calls is a root of the analysis rather than an
// exemption.
func TestLinknameCgoAsmPluginDetectorRetainsNoCgoExport(t *testing.T) {
	in := inputOf(t, "cgo-export.txtar", Options{})

	if len(in.Result.ExcludedByCgo) != 0 {
		t.Fatalf("Setup: load(cgo-export.txtar).ExcludedByCgo = %q, want empty",
			in.Result.ExcludedByCgo)
	}

	got, err := LinknameCgoAsmPluginDetector(in)
	if err != nil {
		t.Fatalf("LinknameCgoAsmPluginDetector(cgo-export.txtar) error: %v", err)
	}
	if lines := rendered(in.Symbols, got); len(lines) != 0 {
		t.Errorf("LinknameCgoAsmPluginDetector(cgo-export.txtar) = %q, want none", lines)
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
