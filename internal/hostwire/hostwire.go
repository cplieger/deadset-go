// Package hostwire is the line protocol of the command's host form (the
// deadset_host build of cmd/deadset-go): one JSON object per line each way. Its
// client is internal/testsupport/host.
package hostwire

// Request is one line of the host's input: an invocation run from Dir, or, with
// Cancel, the end of the running request ID, which the run sees as an interrupt.
type Request struct {
	Dir    string   `json:"dir,omitempty"`
	Args   []string `json:"args,omitempty"`
	ID     uint64   `json:"id"`
	Cancel bool     `json:"cancel,omitempty"`
}

// Response is one line of the host's output: what request ID printed and exited
// with.
type Response struct {
	Stderr string `json:"stderr"`
	Stdout []byte `json:"stdout"`
	ID     uint64 `json:"id"`
	Code   int    `json:"code"`
}
