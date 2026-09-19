package kinds

import (
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/graph"
)

// interfaceEmitters is the three interface rules as the composition root's table
// holds them, which is what fixes each of them to the framework's emitter
// signature.
func interfaceEmitters() map[string]Emitter {
	return map[string]Emitter{
		unusedInterfaceCode:             UnusedInterface,
		uncalledInterfaceMethodCode:     UncalledInterfaceMethod,
		unusedSatisfactionAssertionCode: UnusedSatisfactionAssertion,
	}
}

func TestInterfaceKinds_reportEachShapeOnceUnderItsOwnCode(t *testing.T) {
	cases := map[string]struct {
		archive string
		want    []string
	}{
		"an interface nothing names as a type, with its members inside its own component": {
			archive: "interfaces-unused.txtar",
			want:    []string{"DS1201 Reader"},
		},
		"an interface a parameter type names, whose method the function calls through it": {
			archive: "interfaces-used.txtar",
			want:    nil,
		},
		"a method the program calls on the concrete type and never through the interface": {
			archive: "interfaces-uncalled.txtar",
			want:    []string{"DS1203 Store.Put"},
		},
		"an interface whose method set holds an unexported method": {
			archive: "interfaces-sum-type.txtar",
			want:    nil,
		},
		"a marker method whose every implementation carries an empty body": {
			archive: "interfaces-marker.txtar",
			want:    nil,
		},
		"an interface a test file declares, whose liveness a production sweep cannot judge": {
			archive: "interfaces-test-file.txtar",
			want:    nil,
		},
		"a satisfaction assertion that is the only thing naming its interface": {
			archive: "interfaces-assertion.txtar",
			want:    []string{"DS1203 Encoder.Encode", "DS1204 _"},
		},
	}
	for description, tc := range cases {
		t.Run(description, func(t *testing.T) {
			in := inputOf(t, tc.archive, applicationConfig(), Consumers{})

			result := computed(t, in, interfaceEmitters())

			if got := summary(result.Findings); !slices.Equal(got, tc.want) {
				t.Errorf("the interface kinds over %s report %v, want %v", tc.archive, got, tc.want)
			}
		})
	}
}

func TestUnusedInterface_namesEveryImplementationAndWhereItIsWritten(t *testing.T) {
	in := inputOf(t, "interfaces-unused.txtar", applicationConfig(), Consumers{})

	result := computed(t, in, interfaceEmitters())

	found := findingOf(t, result.Findings, unusedInterfaceCode, "Reader")
	if found.Kind != "unused-interface" || found.Symbol.Kind != "interface" {
		t.Errorf("UnusedInterface(interfaces-unused.txtar) kind, subject kind = %q, %q, want %q, %q",
			found.Kind, found.Symbol.Kind, "unused-interface", "interface")
	}
	if found.Message != unusedInterfaceMessage {
		t.Errorf("UnusedInterface(interfaces-unused.txtar) message = %q, want %q",
			found.Message, unusedInterfaceMessage)
	}
	if found.Relation != graph.ReferenceCounting {
		t.Errorf("UnusedInterface(interfaces-unused.txtar) relation = %s, want %s",
			found.Relation, graph.ReferenceCounting)
	}
	if found.Fixability != "deletable" || found.Severity != config.Deny {
		t.Errorf("UnusedInterface(interfaces-unused.txtar) fixability, severity = %q, %q, want deletable, deny",
			found.Fixability, found.Severity)
	}
	if got, want := implementationNames(found), []string{"file", "socket"}; !slices.Equal(got, want) {
		t.Errorf("UnusedInterface(interfaces-unused.txtar) implementations = %v, want %v", got, want)
	}
	for _, implementation := range found.Details.Implementations {
		if implementation.Position.Path != "main.go" || implementation.Position.Line == 0 {
			t.Errorf("UnusedInterface(interfaces-unused.txtar) implementation %q is at %s:%d, want a position in main.go",
				implementation.Name, implementation.Position.Path, implementation.Position.Line)
		}
		if !strings.HasSuffix(implementation.Ref, "#"+implementation.Name) {
			t.Errorf("UnusedInterface(interfaces-unused.txtar) implementation %q carries the reference %q, want one naming the type",
				implementation.Name, implementation.Ref)
		}
	}
}

func TestUncalledInterfaceMethod_reportsTheInterfaceMethodAndNamesTheImplementation(t *testing.T) {
	in := inputOf(t, "interfaces-uncalled.txtar", applicationConfig(), Consumers{})

	result := computed(t, in, interfaceEmitters())

	found := findingOf(t, result.Findings, uncalledInterfaceMethodCode, "Store.Put")
	if found.Kind != "uncalled-interface-method" || found.Symbol.Kind != "interface-method" {
		t.Errorf("UncalledInterfaceMethod(interfaces-uncalled.txtar) kind, subject kind = %q, %q, want %q, %q",
			found.Kind, found.Symbol.Kind, "uncalled-interface-method", "interface-method")
	}
	if found.Message != uncalledInterfaceMethodMessage {
		t.Errorf("UncalledInterfaceMethod(interfaces-uncalled.txtar) message = %q, want %q",
			found.Message, uncalledInterfaceMethodMessage)
	}
	if found.Relation != graph.ReferenceCounting {
		t.Errorf("UncalledInterfaceMethod(interfaces-uncalled.txtar) relation = %s, want %s",
			found.Relation, graph.ReferenceCounting)
	}
	if found.Fixability != "manual" || found.Severity != config.Warn {
		t.Errorf("UncalledInterfaceMethod(interfaces-uncalled.txtar) fixability, severity = %q, %q, want manual, warn",
			found.Fixability, found.Severity)
	}
	if got, want := implementationNames(found), []string{"disk"}; !slices.Equal(got, want) {
		t.Errorf("UncalledInterfaceMethod(interfaces-uncalled.txtar) implementations = %v, want %v", got, want)
	}
}

func TestUnusedSatisfactionAssertion_reportsTheAssertionWhileTheConcreteMethodStaysRetained(t *testing.T) {
	in := inputOf(t, "interfaces-assertion.txtar", applicationConfig(), Consumers{})

	result := computed(t, in, interfaceEmitters())

	found := findingOf(t, result.Findings, unusedSatisfactionAssertionCode, "_")
	if found.Kind != "unused-satisfaction-assertion" || found.Symbol.Kind != "variable" {
		t.Errorf("UnusedSatisfactionAssertion(interfaces-assertion.txtar) kind, subject kind = %q, %q, want %q, %q",
			found.Kind, found.Symbol.Kind, "unused-satisfaction-assertion", "variable")
	}
	if found.Message != unusedSatisfactionAssertionMessage {
		t.Errorf("UnusedSatisfactionAssertion(interfaces-assertion.txtar) message = %q, want %q",
			found.Message, unusedSatisfactionAssertionMessage)
	}
	if found.Fixability != "deletable" || found.Severity != config.Deny {
		t.Errorf("UnusedSatisfactionAssertion(interfaces-assertion.txtar) fixability, severity = %q, %q, want deletable, deny",
			found.Fixability, found.Severity)
	}
	if got, want := implementationNames(found), []string{"payload"}; !slices.Equal(got, want) {
		t.Errorf("UnusedSatisfactionAssertion(interfaces-assertion.txtar) implementations = %v, want %v", got, want)
	}
	if got, want := retainedUnder(in, "interface-satisfaction"), []string{"payload.Encode"}; !slices.Equal(got, want) {
		t.Errorf("the sweep over interfaces-assertion.txtar retains %v under interface-satisfaction, want %v", got, want)
	}
}

// implementationNames names every implementation one finding carries, in the order
// the finding carries them.
func implementationNames(found Finding) []string {
	names := make([]string, 0, len(found.Details.Implementations))
	for _, implementation := range found.Details.Implementations {
		names = append(names, implementation.Name)
	}
	return names
}

// retainedUnder names every declaration the sweep recorded as held back by one
// exemption class, which is the record a finding's retained field reads.
func retainedUnder(in *Input, class string) []string {
	var names []string
	for _, exemption := range in.Sweep.Retained {
		if exemption.Class != class {
			continue
		}
		if symbol := in.symbol(exemption.ID); symbol != nil {
			names = append(names, symbol.Name)
		}
	}
	return names
}
