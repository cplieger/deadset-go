// Package deadline runs a test row that may hang so that, at its deadline, nothing
// it started is left running: the row runs in a process group of its own, which
// the deadline kills whole, and a named pipe the row made is opened for writing
// before its tree is removed, so no reader stays blocked on it. A child left
// behind keeps every descriptor it inherited, a lock held by the go test run
// included. Only Unix test files import it: the rows it bounds make named pipes,
// which Windows does not have.
package deadline
