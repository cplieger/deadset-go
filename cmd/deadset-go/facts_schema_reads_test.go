package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/deadset-spec/v8/vocab"
)

// consumerStates maps the fragment of each consumer record's symbol, the part
// after its package, to the record's state.
func consumerStates(facts map[string]any) map[string]string {
	states := make(map[string]string)
	bindings, _ := facts["bindings"].([]any)
	for _, one := range bindings {
		record, _ := one.(map[string]any)
		if record["role"] != "consumer" {
			continue
		}
		symbol, _ := record["symbol"].(string)
		_, fragment, _ := strings.Cut(symbol, "#")
		state, _ := record["state"].(string)
		states[fragment] = state
	}
	return states
}

// A hand-written serializer call that encodes or compares reads every field
// reachable from the message type it is handed, a nested, repeated, mapped and
// oneof message included; a decoding call writes and a clone passes its argument
// through, so neither reads; a name conversion of an enum value reads every value
// of the enum (references-go.16, bindings.15). The runtime's own reflection in a
// generated message method reaches no field (bindings.12).
func TestFactsReadWhatAHandWrittenSerializerCallEncodes(t *testing.T) {
	facts, _ := analyzeWithFacts(t, archiveModule(t, "schema-reads/serializers.txtar"))
	got := consumerStates(facts)
	want := map[string]string{
		"Envelope.Label": "live", "Envelope.Item": "live", "Envelope.Items": "live", "Envelope.Named": "live",
		"Envelope_Word": "live", "Envelope_Word.Word": "live", "Envelope_Boxed.Boxed": "live",
		"Item.Number": "live", "Item.Text": "live",
		"Sized.N": "live", "Equaled.N": "live", "Optioned.N": "live", "Formatted.N": "live", "Spun.N": "live",
		"Cloned.N": "unreferenced", "Decoded.N": "unreferenced", "Other.N": "unreferenced",
		"Colour_COLOUR_UNSPECIFIED": "live", "Colour_COLOUR_RED": "live",
		"Shade_SHADE_UNSPECIFIED": "live", "Shade_SHADE_DARK": "live",
		"Tone_TONE_LOW": "live", "Tone_TONE_UNSPECIFIED": "unreferenced",
	}
	for fragment, state := range want {
		if got[fragment] != state {
			t.Errorf("consumer %s state = %q, want %q", fragment, got[fragment], state)
		}
	}
}

// A loaded consumer's production call of a serializer reads what it encodes, an
// actual caller of the target; its test file's call reads nothing for production.
func TestFactsReadWhatALoadedConsumerSerializes(t *testing.T) {
	files := make(map[string]string)
	for name, data := range archiveFiles(t, "schema-reads/serializers.txtar") {
		if name != "main.go" {
			files["target/"+name] = data
		}
	}
	files["target/"+repositoryDocument] = `{"target": {"kind": "library"}, "consumers": {"complete": true}, "analysis": {"min_confidence": "possible"}}`
	files["consumer/go.mod"] = "module example.com/consumer\n\ngo 1.27.0\n\n" +
		"require (\n\texample.com/app v0.0.0\n\tgoogle.golang.org/protobuf v0.0.0\n)\n\n" +
		"replace (\n\texample.com/app => ../target\n\tgoogle.golang.org/protobuf v0.0.0 => ../target/protobuf\n)\n"
	files["consumer/main.go"] = "package main\n\nimport (\n\tv1 \"example.com/app/gen/v1\"\n\t\"google.golang.org/protobuf/proto\"\n)\n\n" +
		"func main() { _, _ = proto.Marshal(&v1.Item{}) }\n"
	files["consumer/main_test.go"] = "package main\n\nimport (\n\t\"testing\"\n\n\tv1 \"example.com/app/gen/v1\"\n\t\"google.golang.org/protobuf/proto\"\n)\n\n" +
		"func TestEncode(t *testing.T) { _, _ = proto.Marshal(&v1.Other{}) }\n"
	files["scope.json"] = `{
  "target": {"id": "example.com/app", "role": "target", "path": "target"},
  "consumers": [{"id": "example.com/consumer", "role": "consumer", "path": "consumer"}]
}
`
	facts := analyzeScopeWithFacts(t, files)
	got := consumerStates(facts)
	for fragment, state := range map[string]string{"Item.Number": "live", "Item.Text": "live", "Other.N": "unreferenced"} {
		if got[fragment] != state {
			t.Errorf("consumer %s state = %q, want %q", fragment, got[fragment], state)
		}
	}
}

// analyzeScopeWithFacts runs analyze --scope=scope.json --facts over the
// directory files lay out, the configuration target/deadset.json, and returns the
// facts document decoded as plain JSON values.
func analyzeScopeWithFacts(t *testing.T, files map[string]string) map[string]any {
	t.Helper()
	t.Chdir(writeModule(t, files))
	factsPath := filepath.Join(t.TempDir(), "facts.json")
	var stdout, stderr bytes.Buffer
	code := invoke(t.Context(), []string{
		"analyze", "--scope=scope.json", "--config=target/" + repositoryDocument,
		"--report=" + filepath.Join(t.TempDir(), "report.json"), "--facts=" + factsPath,
	}, &stdout, &stderr)
	if code == vocab.ExitUsage || code == vocab.ExitFailure {
		t.Fatalf("analyze --scope=scope.json --facts = %d, want a verdict\nstderr:\n%s", code, stderr.String())
	}
	body, err := os.ReadFile(factsPath)
	if err != nil {
		t.Fatalf("analyze --facts wrote no facts document: %v", err)
	}
	var facts map[string]any
	if err := json.Unmarshal(body, &facts); err != nil {
		t.Fatalf("decode the facts document: %v", err)
	}
	return facts
}

// A oneof member is read where hand-written code tells it apart from its
// siblings, and a read of the oneof that tells none apart reads every member; a
// wrapper literal only writes, and a nested message's oneof binds to the nested
// message (references-go.15, bindings.14).
func TestFactsReadAOneofMemberWhereHandCodeTellsItApart(t *testing.T) {
	facts, _ := analyzeWithFacts(t, archiveModule(t, "schema-reads/oneofs.txtar"))
	got := consumerStates(facts)
	want := map[string]string{
		"Choice_Switched_case": "live", "Choice_Switched": "unreferenced",
		"Choice_Defaulted": "unreferenced", "Choice_Defaulted_case": "unreferenced",
		"Choice_Written": "unreferenced", "Choice_Written.Written": "unreferenced", "Choice_Written_case": "unreferenced",
		"Set_Set": "unreferenced", "Set_Set_case": "unreferenced",
		"Has_Has": "live", "Has_Has.Has": "live", "Has_Has_case": "live",
		"Unset_Unset": "live", "Unset_Unset_case": "live",
		"Got_Got": "live", "Got_Got_case": "live",
		"Choice_Inner_Inner": "live", "Choice_Inner_Sibling": "unreferenced",
		"Compared_Compared_case": "live", "Compared_Uncompared": "unreferenced", "Compared_Uncompared_case": "unreferenced",
	}
	for fragment, state := range want {
		if got[fragment] != state {
			t.Errorf("consumer %s state = %q, want %q", fragment, got[fragment], state)
		}
	}
	bindings, _ := facts["bindings"].([]any)
	for _, one := range bindings {
		record, _ := one.(map[string]any)
		if symbol, _ := record["symbol"].(string); strings.HasSuffix(symbol, "#Choice_Inner_Inner") && record["schema"] != "proto://v1#Choice.Inner.inner" {
			t.Errorf("Choice_Inner_Inner binds %v, want proto://v1#Choice.Inner.inner", record["schema"])
		}
	}
}
