package exempt

import (
	"slices"
	"testing"
)

func TestReflectiveLookupDetector(t *testing.T) {
	in := inputOf(t, "reflective-lookup.txtar", Options{})

	got, err := ReflectiveLookupDetector(in)
	if err != nil {
		t.Fatalf("ReflectiveLookupDetector(reflective-lookup.txtar) error: %v", err)
	}
	want := []string{
		"(*Server).Reload reflective-lookup server.go:31:6 looked up by reflect.Value.MethodByName",
		"(*Server).Refresh reflective-lookup server.go:32:6 looked up by reflect.Value.MethodByName",
		"(*Server).Reload reflective-lookup server.go:34:9 looked up by reflect.Type.MethodByName",
		"Server.Token reflective-lookup server.go:37:6 looked up by reflect.Value.FieldByName",
		"Server.secret reflective-lookup server.go:38:6 looked up by reflect.Value.FieldByName",
		"Handler reflective-lookup server.go:43:9 looked up by plugin.Plugin.Lookup",
	}
	if rows := retainedRows(t, in, got); !slices.Equal(rows, want) {
		t.Errorf("ReflectiveLookupDetector(reflective-lookup.txtar) = %q, want %q", rows, want)
	}
}

func TestReflectiveLookupDetectorWithoutALookup(t *testing.T) {
	in := inputOf(t, "reflective-lookup-none.txtar", Options{})

	got, err := ReflectiveLookupDetector(in)
	if err != nil {
		t.Fatalf("ReflectiveLookupDetector(reflective-lookup-none.txtar) error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ReflectiveLookupDetector(reflective-lookup-none.txtar) = %q, want no exemption",
			retainedRows(t, in, got))
	}
}
