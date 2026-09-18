// Package platform selects one of two files on the operating system, so a matrix
// of two configurations loads two different sets of source and both type-check.
package platform

// Selected is compiled under every configuration and calls the file the
// platform selects.
func Selected(name string) string { return render(name) }
