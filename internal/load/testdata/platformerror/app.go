// Package platformerror type-checks under the linux configuration and not under
// the windows one, so a matrix that loads linux first fails at its second
// configuration.
package platformerror

// Selected is compiled under every configuration and calls the file the
// platform selects.
func Selected(name string) string { return render(name) }
