//go:build windows

package platformerror

// render promises a string and returns the length of its argument, so the
// windows configuration does not type-check.
func render(name string) string { return len(name) }
