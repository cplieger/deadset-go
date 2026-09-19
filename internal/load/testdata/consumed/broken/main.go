// Command broken does not type-check: it assigns text to an integer.
package main

import "example.com/consumed"

func main() {
	var count int = consumed.Used()
	_ = count
}
