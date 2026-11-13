//go:build deadset_names

package scope

// A command built with the deadset_names tag checks the names it parses files
// under, as a test binary does ([namesChecked]).
func init() { namesChecked = true }
