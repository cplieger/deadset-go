// Command uncached imports a module no local cache holds.
package main

import "example.com/absent"

func main() {
	absent.Call()
}
