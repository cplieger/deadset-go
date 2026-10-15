//go:build !linux && !darwin && !windows

package memory

// Platform reads nothing where no reader is implemented, so the governor sets
// nothing.
func Platform() Machine {
	return Machine{}
}
