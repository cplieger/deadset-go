package memory

// PlatformSources are the readers of this platform, in the order they are read:
// cgroup v2, cgroup v1, then MemAvailable.
func PlatformSources() []Source {
	return linuxSources("/")
}
