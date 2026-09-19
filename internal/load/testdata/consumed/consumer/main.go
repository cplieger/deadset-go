// Command consumer references one exported declaration of the target module.
package main

import "example.com/consumed"

func main() {
	if consumed.Used() == "" {
		panic("the target returned an empty string")
	}
}
