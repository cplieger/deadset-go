package main

import "testing"

// TestTheVersionIsTheModuleVersionTheBuildStamped pins what each shape the
// toolchain stamps becomes, and that every one of them is a version the report
// schema accepts.
//
// The analyzer's version reaches a committed document and a report a consumer
// validates, so a build the toolchain has no version for has to answer with a
// version rather than with the toolchain's own placeholder.
func TestTheVersionIsTheModuleVersionTheBuildStamped(t *testing.T) {
	t.Parallel()

	pattern := analyzerPattern(t, "version")
	for name, one := range map[string]struct {
		stamped string
		want    string
	}{
		"the tag under go install": {
			stamped: "v1.9.1", want: "1.9.1",
		},
		"the pseudo-version under go build in a checkout": {
			stamped: "v1.9.1-0.20260919143000-abcdef123456", want: "1.9.1-0.20260919143000-abcdef123456",
		},
		"a build of a tree holding uncommitted changes": {
			stamped: "v1.9.0+dirty", want: "1.9.0+dirty",
		},
		"a build the toolchain records no version for": {
			stamped: "(devel)", want: develVersion,
		},
		"a build carrying no module version at all": {
			stamped: "", want: develVersion,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := versionOf(one.stamped)
			if got != one.want {
				t.Errorf("versionOf(%q) = %q, want %q", one.stamped, got, one.want)
			}
			if !pattern.MatchString(got) {
				t.Errorf("versionOf(%q) = %q, which analyzer.version of the report schema refuses",
					one.stamped, got)
			}
		})
	}
}

// TestTheVersionOfThisBuildIsOneTheReportSchemaAccepts reads the version of the
// build running the suite, which is the one every document this command writes
// names, and refuses a value the Contract would.
func TestTheVersionOfThisBuildIsOneTheReportSchemaAccepts(t *testing.T) {
	t.Parallel()

	got := version()
	if pattern := analyzerPattern(t, "version"); !pattern.MatchString(got) {
		t.Errorf("version() = %q for the build running this suite, which analyzer.version of the report schema refuses",
			got)
	}
}
