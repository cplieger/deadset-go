package liveness

import (
	"go/token"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/exempt"
	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/roots"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// handGeneratedFile is the generated file a carrier test moves declarations into.
const handGeneratedFile = "gen.go"

func TestCarryFollowsHandWrittenCodeThroughGeneratedCodeAndCutsRegistrationWritesAndDispatch(t *testing.T) {
	t.Parallel()

	b := newGraphBuilder(t)
	b.add("main", "impl", "decorator", "helper", "spare", "assertion")
	b.addSupport("fixture")
	b.add("Register", "Desc", "GenHandler", "Dispatch", "Getter", "Read", "Written")
	b.declare(handSymbol{name: "Server", kind: inventory.KindInterface, lines: 3})
	b.declare(handSymbol{name: "ServerPing", parent: "Server", kind: inventory.KindInterfaceMethod, nested: true})
	for _, name := range []string{"Register", "Desc", "GenHandler", "Dispatch", "Getter", "Read", "Written", "Server", "ServerPing"} {
		b.symbols[b.at(name)].Pos.Filename = handGeneratedFile
	}
	b.symbols[b.at("assertion")].Blank = true
	b.root("main", roots.RootMain)
	b.root("assertion", roots.RootBlank)
	b.root("Desc", roots.RootInit)
	for _, edge := range [][2]string{
		{"main", "Register"},
		{"Register", "Desc"},
		{"Desc", "GenHandler"},
		{"GenHandler", "ServerPing"},
		{"main", "Dispatch"},
		{"Dispatch", "ServerPing"},
		{"main", "Getter"},
		{"Getter", "Read"},
		{"decorator", "ServerPing"},
		{"impl", "helper"},
		{"main", "fixture"},
		{"fixture", "spare"},
	} {
		b.ref(edge[0], edge[1])
	}
	b.refs = append(b.refs,
		facts.Reference{From: b.id("main"), To: b.id("Written"), Kind: facts.RefWrite},
		facts.Reference{From: b.id("main"), To: b.id("Server"), Kind: facts.RefConversion},
		facts.Reference{From: b.id("assertion"), To: b.id("Server"), Kind: facts.RefTypeUse},
	)
	satisfaction := exempt.Exemption{
		ID: b.id("impl"), Class: string(vocab.InterfaceSatisfaction), Via: b.id("ServerPing"), Holder: b.id("main"),
		Site: token.Position{Filename: handFile, Line: 1},
	}
	declared := exempt.Exemption{
		ID: b.id("GenHandler"), Class: string(vocab.GeneratedFile), Site: token.Position{Filename: handGeneratedFile, Line: 1},
	}
	k := b.graph().Carry(&Carrier{
		Generated:     map[string]bool{handGeneratedFile: true},
		Implementers:  map[inventory.SymbolID][]inventory.SymbolID{b.id("ServerPing"): {b.id("impl"), b.id("decorator")}},
		Registrations: map[inventory.SymbolID]bool{b.id("Register"): true},
		Exempt:        []exempt.Exemption{satisfaction, declared},
	})

	for name, want := range map[string]bool{
		"main": true, "Register": true, "Dispatch": true, "Getter": true, "Read": true,
		"Desc": false, "GenHandler": false, "ServerPing": false, "Written": false, "Server": false, "assertion": true,
		"impl": false, "decorator": false, "helper": false, "spare": false,
	} {
		if got := k.Hand().Holds(b.id(name)); got != want {
			t.Errorf("Carry().Hand().Holds(%s) = %t, want %t", name, got, want)
		}
	}
	for name, want := range map[string]bool{"ServerPing": true, "impl": true, "helper": true, "main": false} {
		if got := k.Search(b.id("decorator")).Holds(b.id(name)); got != want {
			t.Errorf("Search(decorator).Holds(%s) = %t, want %t", name, got, want)
		}
	}
	if k.Search(b.id("impl")).Holds(b.id("decorator")) {
		t.Errorf("Search(impl).Holds(decorator) = true, want false: an implementer reaches no other through dispatch")
	}
	if !k.Answers(&satisfaction) || k.Answers(&declared) {
		t.Errorf("Answers(satisfaction, generated-file) = %t, %t, want true, false", k.Answers(&satisfaction), k.Answers(&declared))
	}
}

// at is the place in the builder's symbols of one declared name.
func (b *graphBuilder) at(name string) int {
	b.sink.Helper()
	id := b.id(name)
	for i := range b.symbols {
		if b.symbols[i].ID == id {
			return i
		}
	}
	return -1
}
