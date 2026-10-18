package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// execArgsVariable carries, as a JSON array, the arguments a re-executed test binary
// runs governed with instead of its tests, so a test can signal a real process.
const execArgsVariable = "DEADSET_GO_TEST_EXEC_ARGS"

// classLines is every line of stderr a group prefix of failure_classes opens.
func classLines(stderr string) []string {
	var held []string
	for line := range strings.SplitSeq(stderr, "\n") {
		if strings.HasPrefix(line, vocab.SetupFailurePrefix) || strings.HasPrefix(line, vocab.RunFailurePrefix) {
			held = append(held, line)
		}
	}
	return held
}

// wantOneClassLine fails the test unless stderr holds exactly one class line and it
// opens with prefix.
func wantOneClassLine(t *testing.T, what, stderr, prefix string) {
	t.Helper()
	lines := classLines(stderr)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], prefix) {
		t.Errorf("%s printed the class lines %q, want exactly one opening %q\nstderr:\n%s", what, lines, prefix, stderr)
	}
}

func TestTwoMissingModulesPrintOneClassLine(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"go.mod":           "module example.com/app\n\ngo 1.27.1\n",
		"main.go":          "package main\n\nimport (\n\t\"example.com/app/gen1\"\n\t\"example.com/app/gen2\"\n)\n\nfunc main() { gen1.F(); gen2.G() }\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	})

	got := runAnalyze(t, dir)
	if got.code != vocab.ExitFailure {
		t.Fatalf("analyze of two missing modules = %d, want %d\nstderr:\n%s", got.code, vocab.ExitFailure, got.stderr)
	}
	wantOneClassLine(t, "analyze of two missing modules", got.stderr, vocab.SetupFailurePrefix+vocab.SetupMissingModule+": main.go:4:")
	if !strings.Contains(got.stderr, "\n"+vocab.SetupMissingModule+": main.go:5:") {
		t.Errorf("analyze of two missing modules wrote %q, want the second import on a line opening %q", got.stderr, vocab.SetupMissingModule+": main.go:5:")
	}
}

func TestAnUnreadableTargetDocumentEndsTheRunWithItsSetupClass(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "ignore.json"), []byte(`{"ignore": []}`), 0o600); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	tests := []struct {
		name, document string
		make           func(path string) error
	}{
		{"ignore-file-a-directory", "deadset-ignore.json", func(p string) error { return os.Mkdir(p, 0o750) }},
		{"baseline-a-directory", "deadset-baseline.json", func(p string) error { return os.Mkdir(p, 0o750) }},
		{"edges-document-a-directory", vocab.EdgesFileName, func(p string) error { return os.Mkdir(p, 0o750) }},
		{"ignore-file-a-link-outside-the-target", "deadset-ignore.json", func(p string) error {
			return os.Symlink(filepath.Join(outside, "ignore.json"), p)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeModule(t, map[string]string{
				"go.mod":           "module example.com/app\n\ngo 1.27.1\n",
				"main.go":          "package main\n\nfunc main() {}\n",
				repositoryDocument: `{"target": {"kind": "application"}}`,
			})
			if err := tc.make(filepath.Join(dir, tc.document)); err != nil {
				t.Fatalf("Setup: %v", err)
			}
			got := runAnalyze(t, dir)
			if got.code != vocab.ExitFailure {
				t.Fatalf("analyze with an unreadable %s = %d, want %d\nstderr:\n%s", tc.document, got.code, vocab.ExitFailure, got.stderr)
			}
			wantOneClassLine(t, "analyze with an unreadable "+tc.document, got.stderr,
				vocab.SetupFailurePrefix+vocab.SetupUnreadableDocument+": "+tc.document)
			if _, err := os.Stat(got.reportPath); !os.IsNotExist(err) {
				t.Errorf("analyze with an unreadable %s wrote a report (stat: %v), want none", tc.document, err)
			}
		})
	}
}

// failingWriter refuses every write, as a closed or full standard output does.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("no space left on device") }

func TestAnUnwrittenOutputEndsTheRunWithItsRunClass(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"go.mod":           "module example.com/app\n\ngo 1.27.1\n",
		"main.go":          "package main\n\nfunc main() {}\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	})
	absent := filepath.Join(t.TempDir(), "absent")
	prefix := vocab.RunFailurePrefix + vocab.RunUnwrittenOutput + ": "

	t.Run("the-report", func(t *testing.T) {
		t.Chdir(dir)
		var stdout, stderr bytes.Buffer
		code := invoke(t.Context(), []string{"analyze", "--target=.", "--report=" + filepath.Join(absent, "report.json")}, &stdout, &stderr)
		if code != vocab.ExitFailure {
			t.Fatalf("analyze to a missing directory = %d, want %d\nstderr:\n%s", code, vocab.ExitFailure, stderr.String())
		}
		wantOneClassLine(t, "analyze to a missing directory", stderr.String(), prefix)
		if !strings.Contains(stderr.String(), "absent/report.json") {
			t.Errorf("analyze to a missing directory wrote %q, want the report path named", stderr.String())
		}
	})
	t.Run("the-baseline", func(t *testing.T) {
		t.Chdir(dir)
		var stdout, stderr bytes.Buffer
		code := invoke(t.Context(), []string{
			"analyze", "--target=.", "--report=" + filepath.Join(t.TempDir(), "report.json"),
			"--baseline-write=" + filepath.Join(absent, "baseline.json"),
		}, &stdout, &stderr)
		if code != vocab.ExitFailure {
			t.Fatalf("analyze writing a baseline to a missing directory = %d, want %d\nstderr:\n%s", code, vocab.ExitFailure, stderr.String())
		}
		wantOneClassLine(t, "analyze writing a baseline to a missing directory", stderr.String(), prefix)
	})
	t.Run("a-baseline-path-that-is-a-directory", func(t *testing.T) {
		t.Chdir(dir)
		directory := t.TempDir()
		var stdout, stderr bytes.Buffer
		code := invoke(t.Context(), []string{
			"analyze", "--target=.", "--report=" + filepath.Join(t.TempDir(), "report.json"), "--baseline-write=" + directory,
		}, &stdout, &stderr)
		if code != vocab.ExitFailure {
			t.Fatalf("analyze writing a baseline over a directory = %d, want %d\nstderr:\n%s", code, vocab.ExitFailure, stderr.String())
		}
		wantOneClassLine(t, "analyze writing a baseline over a directory", stderr.String(), prefix)
		if strings.Contains(stderr.String(), vocab.SetupFailurePrefix) {
			t.Errorf("analyze writing a baseline over a directory wrote %q, want no setup failure", stderr.String())
		}
	})
	t.Run("the-version-verbs-standard-output", func(t *testing.T) {
		var stderr bytes.Buffer
		code := invoke(t.Context(), []string{"version"}, failingWriter{}, &stderr)
		if code != vocab.ExitFailure {
			t.Fatalf("version to a failing stdout = %d, want %d\nstderr:\n%s", code, vocab.ExitFailure, stderr.String())
		}
		wantOneClassLine(t, "version to a failing stdout", stderr.String(), prefix)
	})
	t.Run("a-verbs-standard-output", func(t *testing.T) {
		var stderr bytes.Buffer
		code := invoke(t.Context(), []string{"print-roots", "--target=" + dir}, failingWriter{}, &stderr)
		if code != vocab.ExitFailure {
			t.Fatalf("print-roots to a failing stdout = %d, want %d\nstderr:\n%s", code, vocab.ExitFailure, stderr.String())
		}
		wantOneClassLine(t, "print-roots to a failing stdout", stderr.String(), prefix)
	})
}

// A target outside the directory the run was invoked from has no spelling the report
// admits.
func TestATargetOutsideTheRunDirectoryEndsTheRunWithItsRunClass(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"go.mod":           "module example.com/app\n\ngo 1.27.1\n",
		"main.go":          "package main\n\nfunc main() {}\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	})
	t.Chdir(t.TempDir())
	var stdout, stderr bytes.Buffer
	code := invoke(t.Context(), []string{"analyze", "--target=" + dir, "--report=" + filepath.Join(t.TempDir(), "report.json")}, &stdout, &stderr)
	if code != vocab.ExitFailure {
		t.Fatalf("analyze of a target outside the run directory = %d, want %d\nstderr:\n%s", code, vocab.ExitFailure, stderr.String())
	}
	wantOneClassLine(t, "analyze of a target outside the run directory", stderr.String(), vocab.RunFailurePrefix+vocab.RunUnreportablePath+": ")
}

func TestAnErrorNoClassNamesIsAProductFault(t *testing.T) {
	var stderr bytes.Buffer
	code := failed(t.Context(), &stderr, errors.New("the loader returned no package for a listed file"))
	if code != vocab.ExitFailure {
		t.Errorf("failed(an unclassed error) = %d, want %d", code, vocab.ExitFailure)
	}
	wantOneClassLine(t, "failed(an unclassed error)", stderr.String(), vocab.RunFailurePrefix+vocab.RunProductFault+": deadset-go ")
	if got := stderr.String(); !strings.Contains(got, "the loader returned no package for a listed file") || !strings.Contains(got, "reinstall deadset-go") {
		t.Errorf("failed(an unclassed error) printed %q, want the error and the class's fix", got)
	}
}

// reexecuted runs this test binary as the analyzer with args, from dir, under env.
func reexecuted(t *testing.T, dir string, env []string, args ...string) *exec.Cmd {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	cmd := exec.Command(os.Args[0])
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), env...), execArgsVariable+"="+string(encoded))
	return cmd
}

func TestASignalledRunEndsWithTheInterruptedRunClass(t *testing.T) {
	for _, tc := range []struct {
		signal syscall.Signal
		named  string
	}{
		{syscall.SIGINT, "interrupt"},
		{syscall.SIGTERM, "terminated"},
	} {
		t.Run(tc.named, func(t *testing.T) {
			dir := writeModule(t, map[string]string{
				"go.mod":           "module example.com/app\n\ngo 1.27.1\n",
				"main.go":          "package main\n\nfunc main() {}\n",
				repositoryDocument: `{"target": {"kind": "application"}}`,
			})
			bin := t.TempDir()
			started := filepath.Join(dir, "started")
			stub := "#!/bin/sh\ntouch '" + started + "'\nexec sleep 60\n"
			if err := os.WriteFile(filepath.Join(bin, "go"), []byte(stub), 0o700); err != nil {
				t.Fatalf("Setup: %v", err)
			}
			reportPath := filepath.Join(t.TempDir(), "report.json")
			cmd := reexecuted(t, dir, []string{"PATH=" + bin + ":/usr/bin:/bin"}, "analyze", "--target=.", "--report="+reportPath)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatalf("Setup: start the analyzer: %v", err)
			}
			deadline := time.Now().Add(30 * time.Second)
			for {
				if _, err := os.Stat(started); err == nil {
					break
				}
				if time.Now().After(deadline) {
					_ = cmd.Process.Kill()
					t.Fatalf("the analyzer never ran the toolchain\nstderr:\n%s", stderr.String())
				}
				time.Sleep(20 * time.Millisecond)
			}
			if err := cmd.Process.Signal(tc.signal); err != nil {
				t.Fatalf("Setup: signal the analyzer: %v", err)
			}
			_ = cmd.Wait()
			if code := cmd.ProcessState.ExitCode(); code != vocab.ExitFailure {
				t.Fatalf("a run sent %s ended with %d (%v), want %d\nstderr:\n%s", tc.named, code, cmd.ProcessState, vocab.ExitFailure, stderr.String())
			}
			wantOneClassLine(t, "a run sent "+tc.named, stderr.String(), vocab.RunFailurePrefix+vocab.RunInterruptedRun+": "+tc.named)
			if _, err := os.Stat(reportPath); !os.IsNotExist(err) {
				t.Errorf("a run sent %s wrote a report (stat: %v), want none", tc.named, err)
			}
		})
	}
}

func TestAConfigurationRefusedByNoUsageErrorEndsWithOneClassLine(t *testing.T) {
	var stderr bytes.Buffer
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.Usage = func() { fmt.Fprintln(&stderr, "usage") }
	f := &configuredFlags{set: set}

	code := f.refused(t.Context(), &stderr, fmt.Errorf("%w: the repository configuration", config.ErrNoSourceLabel))
	if code != vocab.ExitFailure {
		t.Fatalf("a resolution refused for an unlabeled document = %d, want %d\nstderr:\n%s", code, vocab.ExitFailure, stderr.String())
	}
	wantOneClassLine(t, "a resolution refused for an unlabeled document", stderr.String(), vocab.RunFailurePrefix+vocab.RunProductFault+": ")
	if strings.Contains(stderr.String(), "deadset-go: ") || strings.Contains(stderr.String(), "usage") {
		t.Errorf("a resolution refused for an unlabeled document wrote %q, want the class line alone", stderr.String())
	}

	stderr.Reset()
	if code := f.refused(t.Context(), &stderr, &config.Error{Message: "min_confidence is not set"}); code != vocab.ExitUsage {
		t.Fatalf("a resolution refused for a configuration error = %d, want %d", code, vocab.ExitUsage)
	}
	if want := "deadset-go: min_confidence is not set\nusage\n"; stderr.String() != want {
		t.Errorf("a resolution refused for a configuration error wrote %q, want %q", stderr.String(), want)
	}
}
