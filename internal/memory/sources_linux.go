package memory

// Platform is this machine: its readers, in the order they are read, are cgroup
// v2, cgroup v1, then MemAvailable, and its limit the tightest cgroup memory.max
// (v1 memory.limit_in_bytes), else MemTotal.
func Platform() Machine {
	return linux("/")
}
