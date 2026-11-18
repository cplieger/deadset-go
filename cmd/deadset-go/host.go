//go:build deadset_host

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/cplieger/deadset-go/v2/internal/hostwire"
	"github.com/cplieger/deadset-go/v2/internal/memory"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// The host form of the command, which this module's tests build with the
// deadset_host tag: one process answers every invocation its input asks for, each
// from the directory its request names, so a suite pays once for the declaration
// checks the analyses share ([load.processDeclarations]) instead of once per
// process. The product binary links none of this file.
func init() {
	os.Exit(serve(os.Stdin, os.Stdout, os.Stderr))
}

// errInputEnded is the cause serve cancels its runs with once its input ends:
// nobody is left to read their answers.
var errInputEnded = errors.New("the host's input ended")

// serve answers the requests in until it ends, each on its own goroutine through
// invoke, under one memory governor and the signals governed stops a run on.
//
// Input ending cancels every run still running and serve returns once each has
// stopped, 0 where in ended cleanly and 3 where a request did not decode or an
// answer was not written. The governor or a signal ending the runs ends the host:
// it takes no further request, writes no further answer, prints the failure line
// governed would and returns 3, so a client sees one cause rather than one
// cancelled run per request.
func serve(in io.Reader, out, stderr io.Writer) int {
	signalled, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// A reader gone mid-answer is an error from the write rather than SIGPIPE,
	// which on standard output ends the process before its runs stop.
	signal.Ignore(syscall.SIGPIPE)
	ctx, cancel := context.WithCancelCause(signalled)
	defer cancel(nil)
	govern := memory.Start(ctx, cancel, memory.Platform())
	defer govern()

	var (
		mu      sync.Mutex // guards running alone, never held across a pipe's I/O
		running = map[uint64]context.CancelFunc{}
		written sync.Mutex // serialises the answers' writes, which may block on the pipe
		answers = json.NewEncoder(out)
		broken  atomic.Bool
		wg      sync.WaitGroup
	)
	requests := make(chan hostwire.Request)
	go func() {
		defer close(requests)
		decoder := json.NewDecoder(in)
		for {
			var req hostwire.Request
			if err := decoder.Decode(&req); err != nil {
				if !errors.Is(err, io.EOF) {
					fmt.Fprintf(stderr, "deadset-go host: read a request: %v\n", err)
					broken.Store(true)
				}
				return
			}
			select {
			case requests <- req:
			case <-ctx.Done():
				return
			}
		}
	}()

	for taking := true; taking; {
		select {
		case <-ctx.Done():
			taking = false
		case req, open := <-requests:
			switch {
			case !open:
				cancel(errInputEnded)
				taking = false
			case req.Cancel:
				mu.Lock()
				if end, held := running[req.ID]; held {
					end()
				}
				mu.Unlock()
			default:
				one, end := context.WithCancel(ctx)
				mu.Lock()
				running[req.ID] = end
				mu.Unlock()
				wg.Go(func() {
					var stdout, errOut bytes.Buffer
					answered := hostwire.Response{ID: req.ID, Code: invoke(one, req.Dir, req.Args, &stdout, &errOut)}
					answered.Stdout, answered.Stderr = stdout.Bytes(), errOut.String()
					mu.Lock()
					delete(running, req.ID)
					mu.Unlock()
					end()
					written.Lock()
					defer written.Unlock()
					if ctx.Err() != nil {
						return
					}
					if err := answers.Encode(answered); err != nil {
						fmt.Fprintf(stderr, "deadset-go host: write the answer to request %d: %v\n", req.ID, err)
						broken.Store(true)
					}
				})
			}
		}
	}
	wg.Wait()
	if cause := context.Cause(ctx); !errors.Is(cause, errInputEnded) {
		return failed(ctx, stderr, cause)
	}
	if broken.Load() {
		return vocab.ExitFailure
	}
	return vocab.ExitClean
}
