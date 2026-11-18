// Package host runs the command's host form, cmd/deadset-go built with the
// deadset_host tag, and answers invocations through it: one process serves every
// invocation of a test package instead of one process each, so the declaration
// checks the analyses share are paid once. An answer is what a process run from
// the request's directory would print and exit with, except that a relative path
// the invocation names is spelled joined to that directory wherever the answer
// names it.
package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/cplieger/deadset-go/v2/internal/hostwire"
)

// Build builds the host form of the command package in dir at binary, with no
// version control state stamped, for the platform the caller runs on whatever the
// environment pins its children to, and with tags beside deadset_host. The build
// ends with ctx.
func Build(ctx context.Context, dir, binary string, tags ...string) error {
	if runtime.GOOS == "windows" && !strings.HasSuffix(binary, ".exe") {
		return fmt.Errorf("build the host at %s: a Windows binary's name ends in .exe", binary)
	}
	build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-trimpath", //nolint:gosec // G204: the module's own command
		"-tags="+strings.Join(append([]string{"deadset_host"}, tags...), ","), "-o", binary, ".")
	build.Dir = dir
	build.Env = append(build.Environ(), "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH)
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("build the host: %w\n%s", err, out)
	}
	return nil
}

// Host is one running host process.
type Host struct {
	cmd *exec.Cmd
	// done closes once the host's output ends and it exited, after err and exit
	// are set.
	done    chan struct{}
	pending map[uint64]chan hostwire.Response
	input   io.WriteCloser
	err     error
	exit    error
	stderr  lockedBuffer
	mu      sync.Mutex // guards next and pending, never held across a pipe's I/O
	written sync.Mutex // serialises the requests' writes, which may block on the pipe
	next    uint64
}

// Start runs binary as a host from dir, which holds nothing, so a read of the
// host's own working directory finds nothing there, with env added to the
// environment the caller's own children take. The host stays in the caller's
// process group, so whatever stops the caller's group stops it, and it ends once
// its input does: at [Host.Close], or when the caller dies and the input's write
// end closes with it, which cancels every run still running.
func Start(binary, dir string, env []string) (*Host, error) {
	//nolint:noctx // the host ends with its input, not a context
	h := &Host{cmd: exec.Command(binary), done: make(chan struct{}), pending: map[uint64]chan hostwire.Response{}}
	h.cmd.Dir = dir
	h.cmd.Env = append(h.cmd.Environ(), env...)
	h.cmd.Stderr = &h.stderr
	var err error
	if h.input, err = h.cmd.StdinPipe(); err != nil {
		return nil, fmt.Errorf("start the host: %w", err)
	}
	output, err := h.cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("start the host: %w", err)
	}
	if err := h.cmd.Start(); err != nil {
		return nil, fmt.Errorf("start the host: %w", err)
	}
	go h.read(output)
	return h, nil
}

// read hands every answer of output to the request waiting on it, and once output
// ends waits for the host, so what it wrote to stderr is whole, and records why it
// stopped answering.
func (h *Host) read(output io.Reader) {
	answers := json.NewDecoder(output)
	for {
		var answer hostwire.Response
		if err := answers.Decode(&answer); err != nil {
			h.exit = h.cmd.Wait()
			h.err = fmt.Errorf("the host stopped answering: %w (%v); its stderr: %s", err, h.exit, h.stderr.String())
			close(h.done)
			return
		}
		h.mu.Lock()
		waiting, held := h.pending[answer.ID]
		delete(h.pending, answer.ID)
		h.mu.Unlock()
		if held {
			waiting <- answer
		}
	}
}

// Run answers one invocation run from dir, an absolute directory, or the host's
// own where empty: what it printed and the code it exited with. Where ctx ends
// first, the host ends the run as an interrupt does and Run answers what the run
// then printed. err is a host that did not answer.
func (h *Host) Run(ctx context.Context, dir string, args []string) (stdout []byte, stderr string, code int, err error) {
	if dir != "" && !filepath.IsAbs(dir) {
		return nil, "", 0, fmt.Errorf("run %q from %q: the host reads an invocation from an absolute directory", args, dir)
	}
	answer := make(chan hostwire.Response, 1)
	h.mu.Lock()
	h.next++
	id := h.next
	h.pending[id] = answer
	h.mu.Unlock()
	if !h.send(hostwire.Request{ID: id, Dir: dir, Args: args}) {
		<-h.done
		return nil, "", 0, h.err
	}
	select {
	case got := <-answer:
		return got.Stdout, got.Stderr, got.Code, nil
	case <-h.done:
	case <-ctx.Done():
		h.send(hostwire.Request{ID: id, Cancel: true})
		select {
		case got := <-answer:
			return got.Stdout, got.Stderr, got.Code, nil
		case <-h.done:
		}
	}
	select {
	case got := <-answer:
		return got.Stdout, got.Stderr, got.Code, nil
	default:
		return nil, "", 0, h.err
	}
}

// Invoke is Run as corpus.Analyzer.InProcess asks for it: a host that did not
// answer is the line naming why on stderr, with exit code -1, which no run exits
// with.
func (h *Host) Invoke(ctx context.Context, dir string, args []string) (stdout []byte, stderr string, code int) {
	stdout, stderr, code, err := h.Run(ctx, dir, args)
	if err != nil {
		return nil, "deadset-go host: " + err.Error() + "\n", -1
	}
	return stdout, stderr, code
}

// send writes one request and reports whether the host took it, which it does
// until it stops reading.
func (h *Host) send(req hostwire.Request) bool {
	line, err := json.Marshal(req)
	if err != nil {
		panic(fmt.Sprintf("encode a host request: %v", err)) // a struct of strings, a number and a bool always encodes
	}
	h.written.Lock()
	defer h.written.Unlock()
	_, err = h.input.Write(append(line, '\n'))
	return err == nil
}

// Close ends the host's input and waits for the host to exit. It is called once
// no Run is in flight: the input ending cancels any run still running, whose
// answer the host then never writes. The error is the host's exit failure,
// naming what it wrote to stderr.
func (h *Host) Close() error {
	err := h.input.Close()
	<-h.done
	if err = errors.Join(err, h.exit); err != nil {
		return fmt.Errorf("close the host: %w; its stderr: %s", err, h.stderr.String())
	}
	return nil
}

// Signal sends sig to the host process, which a test of how the host ends sends
// in place of a terminal or a supervisor.
func (h *Host) Signal(sig os.Signal) error {
	return h.cmd.Process.Signal(sig)
}

// lockedBuffer is a buffer the host process and the reader of its failure share.
type lockedBuffer struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
