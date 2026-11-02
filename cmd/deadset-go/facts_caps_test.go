package main

import (
	"maps"
	"strings"
	"testing"
)

// consumerCaps maps the fragment of each consumer record's symbol, the part after
// its package, to the record's cap, empty where it carries none.
func consumerCaps(facts map[string]any) map[string]string {
	caps := make(map[string]string)
	bindings, _ := facts["bindings"].([]any)
	for _, one := range bindings {
		record, _ := one.(map[string]any)
		if record["role"] != "consumer" {
			continue
		}
		symbol, _ := record["symbol"].(string)
		_, fragment, _ := strings.Cut(symbol, "#")
		capped, _ := record["cap"].(string)
		caps[fragment] = capped
	}
	return caps
}

// capsOf is the consumer caps analyze publishes for the caps archive with each
// file of replaced written over the archive's.
func capsOf(t *testing.T, replaced map[string]string) map[string]string {
	t.Helper()
	files := archiveFiles(t, "schema-reads/caps.txtar")
	maps.Copy(files, replaced)
	facts, _ := analyzeWithFacts(t, writeModule(t, files))
	return consumerCaps(facts)
}

// wantCaps reports every consumer whose cap in got is not the one want names.
func wantCaps(t *testing.T, got, want map[string]string) {
	t.Helper()
	for fragment, capped := range want {
		if _, published := got[fragment]; !published {
			t.Errorf("no consumer record %s", fragment)
		} else if got[fragment] != capped {
			t.Errorf("consumer %s cap = %q, want %q", fragment, got[fragment], capped)
		}
	}
}

// decodes is a command decoding messages from ProtoJSON keeping unknown fields,
// with them discarded, and from the binary form, and a test decoding one more.
var decodes = map[string]string{
	"main.go": `package main

import (
	v1 "example.com/app/gen/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func main() {
	data := []byte("{}")
	_ = protojson.Unmarshal(data, &v1.Envelope{})
	_ = protojson.UnmarshalOptions{}.Unmarshal(data, &v1.Sized{})
	_ = (&protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, &v1.Decoded{})
	options := protojson.UnmarshalOptions{DiscardUnknown: true}
	options.DiscardUnknown = false
	_ = options.Unmarshal(data, &v1.Optioned{})
	_ = proto.Unmarshal(data, &v1.Other{})
}
`,
	"main_test.go": `package main

import (
	"testing"

	v1 "example.com/app/gen/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestDecode(t *testing.T) { _ = protojson.Unmarshal(nil, &v1.Cloned{}) }
`,
}

// A consumer of a message hand code decodes from ProtoJSON keeping unknown
// fields, or of a message whose fields reach one, carries cap persistence, its
// getters included, and so does one decoded through options that are no literal
// discarding them; one decoded with unknown fields discarded, from the binary
// form or in a test only carries none, and neither does an enum value
// (references-go.17).
func TestFactsCapAConsumerOfAMessageDecodedFromProtoJSONKeepingUnknownFields(t *testing.T) {
	wantCaps(t, capsOf(t, decodes), map[string]string{
		"Envelope": "persistence", "Envelope.Label": "persistence", "Envelope.GetLabel": "persistence",
		"Envelope.Items": "persistence", "Envelope_Word.Word": "persistence", "Envelope_Boxed": "persistence",
		"Item": "persistence", "Item.Number": "persistence", "Item.GetText": "persistence",
		"Sized.N": "persistence", "Optioned.N": "persistence",
		"Decoded.N": "", "Other.N": "", "Cloned.N": "", "Equaled": "", "Colour_COLOUR_RED": "", "Aside.N": "",
	})
}

func TestFactsCapReadsAnUnkeyedOptionsLiteralByFieldPosition(t *testing.T) {
	wantCaps(t, capsOf(t, map[string]string{
		"main.go": `package main

import (
	v1 "example.com/app/gen/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func main() {
	data := []byte("{}")
	_ = protojson.UnmarshalOptions{true}.Unmarshal(data, &v1.Decoded{})
	_ = (&protojson.UnmarshalOptions{false}).Unmarshal(data, &v1.Sized{})
}
`,
	}), map[string]string{"Decoded.N": "", "Sized.N": "persistence"})
}

// Below a minimum confidence of possible no record carries a cap, because the
// merge must not lower a finding under it (bindings.16).
func TestFactsCapNothingWherePossibleIsBelowTheMinimum(t *testing.T) {
	replaced := maps.Clone(decodes)
	replaced[repositoryDocument] = `{"target": {"kind": "application"}, "analysis": {"min_confidence": "probable"}}`
	for fragment, capped := range capsOf(t, replaced) {
		if capped != "" {
			t.Errorf("consumer %s cap = %q under min_confidence probable, want none", fragment, capped)
		}
	}
}

// A program serving gRPC server reflection exposes the descriptors of every
// generated file it links, and none it does not link, its tests' imports
// included; a test's call serves no program (references-go.17).
func TestFactsCapEveryConsumerOfAFileAProgramServingReflectionLinks(t *testing.T) {
	wantCaps(t, capsOf(t, map[string]string{
		"main.go": `package main

import (
	v1 "example.com/app/gen/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

func main() {
	reflection.Register(grpc.NewServer())
	_ = &v1.Item{}
}
`,
		"aside/aside_test.go": `package aside

import (
	"testing"

	_ "example.com/app/gen/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

func TestServe(t *testing.T) { reflection.Register(grpc.NewServer()) }
`,
		"aside/aside.go": "// Package aside links the second schema file.\npackage aside\n",
		"main_test.go":   "package main\n\nimport (\n\t\"testing\"\n\n\t_ \"example.com/app/gen/v2\"\n)\n\nfunc TestLinked(*testing.T) {}\n",
	}), map[string]string{
		"Item.Number": "descriptor-exposure", "Envelope": "descriptor-exposure", "Decoded.N": "descriptor-exposure",
		"Colour_COLOUR_RED": "descriptor-exposure", "Aside": "", "Aside.N": "",
	})
}

// A package no program of the target links exposes, serving connect's reflection,
// the generated files it links itself (references-go.17).
func TestFactsCapTheFilesAPackageServingConnectReflectionLinks(t *testing.T) {
	wantCaps(t, capsOf(t, map[string]string{
		"serve/serve.go": `// Package serve serves reflection over connect.
package serve

import (
	"connectrpc.com/grpcreflect"
	_ "example.com/app/gen/v2"
)

// Handler serves reflection.
func Handler() (string, any) {
	return grpcreflect.NewHandlerV1Alpha(grpcreflect.NewStaticReflector("v2.Aside"))
}
`,
	}), map[string]string{"Aside.N": "descriptor-exposure", "Aside": "descriptor-exposure", "Item.Number": ""})
}

// A package serving reflection exposes what each program linking it links, not
// only what it imports itself (references-go.17).
func TestFactsCapTheFilesTheProgramLinkingAServingPackageLinks(t *testing.T) {
	wantCaps(t, capsOf(t, map[string]string{
		"main.go": `package main

import (
	"example.com/app/serve"
	v1 "example.com/app/gen/v1"
)

func main() { serve.Serve(); _ = &v1.Item{} }
`,
		"serve/serve.go": `// Package serve serves reflection.
package serve

import (
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

// Serve serves reflection.
func Serve() { reflection.Register(grpc.NewServer()) }
`,
	}), map[string]string{"Item.Number": "descriptor-exposure", "Envelope": "descriptor-exposure", "Aside.N": ""})
}

// Converting one generated file's descriptor to a descriptor message exposes that
// file's consumers only, whatever else the program links (references-go.17).
func TestFactsCapTheConsumersOfTheFileWhoseDescriptorHandCodeConverts(t *testing.T) {
	wantCaps(t, capsOf(t, map[string]string{
		"main.go": `package main

import (
	"fmt"

	v1 "example.com/app/gen/v1"
	v2 "example.com/app/gen/v2"
	"google.golang.org/protobuf/reflect/protodesc"
)

func main() { fmt.Println(protodesc.ToFileDescriptorProto(v2.File_v2_aside_proto), &v1.Item{}) }
`,
	}), map[string]string{"Aside.N": "descriptor-exposure", "Aside.GetN": "descriptor-exposure", "Item.Number": "", "Envelope": ""})
}

// A loaded consumer's program serving reflection exposes the target's generated
// files it links (references-go.17).
func TestFactsCapTheFilesALoadedConsumersProgramServingReflectionLinks(t *testing.T) {
	files := make(map[string]string)
	for name, data := range archiveFiles(t, "schema-reads/caps.txtar") {
		if name != "main.go" {
			files["target/"+name] = data
		}
	}
	files["target/"+repositoryDocument] = `{"target": {"kind": "library"}, "consumers": {"complete": true}, "analysis": {"min_confidence": "possible"}}`
	files["consumer/go.mod"] = "module example.com/consumer\n\ngo 1.27.0\n\n" +
		"require (\n\texample.com/app v0.0.0\n\tgoogle.golang.org/grpc v0.0.0\n\tgoogle.golang.org/protobuf v0.0.0\n)\n\n" +
		"replace (\n\texample.com/app => ../target\n\tgoogle.golang.org/grpc v0.0.0 => ../target/grpc\n" +
		"\tgoogle.golang.org/protobuf v0.0.0 => ../target/protobuf\n)\n"
	files["consumer/main.go"] = "package main\n\nimport (\n\t_ \"example.com/app/gen/v1\"\n\t\"google.golang.org/grpc\"\n" +
		"\t\"google.golang.org/grpc/reflection\"\n)\n\nfunc main() { reflection.Register(grpc.NewServer()) }\n"
	files["scope.json"] = `{
  "target": {"id": "example.com/app", "role": "target", "path": "target"},
  "consumers": [{"id": "example.com/consumer", "role": "consumer", "path": "consumer"}]
}
`
	wantCaps(t, consumerCaps(analyzeScopeWithFacts(t, files)), map[string]string{
		"Item.Number": "descriptor-exposure", "Envelope": "descriptor-exposure", "Aside.N": "",
	})
}
