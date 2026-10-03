//go:build !linux && !darwin && !windows

package memory

// PlatformSources is empty where no reader is implemented, so the governor sets
// nothing.
func PlatformSources() []Source {
	return nil
}
