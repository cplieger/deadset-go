// Package plain imports nothing of the module that depends on it.
package plain

// Width and Height share one line, so Height starts at column 12.
var Width, Height = 1, 2

// Broken returns a string where its signature promises an int, and the error is
// inside its body.
func Broken() int { return "not an int" }
