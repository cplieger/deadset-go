package exempt

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// membersNamed keeps the references naming a member of any of the given types.
func membersNamed(refs []string, typeNames ...string) []string {
	var got []string
	for _, ref := range refs {
		for _, name := range typeNames {
			if strings.Contains(ref, "#"+name+".") {
				got = append(got, ref)
			}
		}
	}
	return got
}

func TestEncodingReflectionReadsTemplatesLoadedThroughAFileSystem(t *testing.T) {
	refs := retainedRefs(t, analysisOf(t, "template-fs-flow.txtar", Options{}), EncodingReflection)
	got := membersNamed(refs, "Page", "Mail")
	want := []string{
		"go://example.com/fsapp#Page.Greeting",
		"go://example.com/fsapp#Page.Label",
		"go://example.com/fsapp#Page.Title",
		"go://example.com/fsapp/report#Mail.Subject",
	}
	if !slices.Equal(got, want) {
		t.Errorf("EncodingReflectionDetector(template-fs-flow.txtar) retained %v, want %v", got, want)
	}
}

func TestEncodingReflectionReadsTemplatesEmbeddedByQuotedPatterns(t *testing.T) {
	refs := retainedRefs(t, analysisOf(t, "template-fs-flow-quoted.txtar", Options{}), EncodingReflection)
	got := membersNamed(refs, "Page")
	want := []string{"go://example.com/quoted#Page.Note", "go://example.com/quoted#Page.Title"}
	if !slices.Equal(got, want) {
		t.Errorf("EncodingReflectionDetector(template-fs-flow-quoted.txtar) retained %v, want %v", got, want)
	}
}

func TestEncodingReflectionReadsEveryMemberWhereAFileSystemTemplateIsUnread(t *testing.T) {
	tests := []struct {
		name    string
		archive string
		want    []string
	}{
		{"computed_directory_and_delimiters", "template-fs-flow-unread.txtar", []string{
			"go://example.com/unread/computed#Page.Body",
			"go://example.com/unread/computed#Page.Title",
			"go://example.com/unread/delims#Card.Back",
			"go://example.com/unread/delims#Card.Title",
		}},
		{"dependency_hands_out_a_template", "template-fs-flow-outside.txtar", []string{
			"go://example.com/themed#Article.Body",
			"go://example.com/themed#Article.Title",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			refs := retainedRefs(t, analysisOf(t, tt.archive, Options{}), EncodingReflection)
			if got := membersNamed(refs, "Page", "Card", "Article"); !slices.Equal(got, tt.want) {
				t.Errorf("EncodingReflectionDetector(%s) retained %v, want %v", tt.archive, got, tt.want)
			}
		})
	}
}

func TestEmbeddedFilesRefusesASetOverTheFileBound(t *testing.T) {
	root := t.TempDir()
	for _, sub := range []string{"one", "two"} {
		if err := os.Mkdir(filepath.Join(root, sub), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	write := func(sub string, count int) {
		for i := range count {
			if err := os.WriteFile(filepath.Join(root, sub, strconv.Itoa(i)+".html"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("one", maxRootFiles/2+1)
	write("two", maxRootFiles/2)
	if files, ok := embeddedFiles(root, root, []string{"one"}); !ok || len(files) != maxRootFiles/2+1 {
		t.Errorf("embeddedFiles(one) = %d files, %v, want %d, true", len(files), ok, maxRootFiles/2+1)
	}
	if files, ok := embeddedFiles(root, root, []string{"one", "two"}); ok {
		t.Errorf("embeddedFiles(one, two) = %d files, true, want false over %d files", len(files), maxRootFiles)
	}
}
