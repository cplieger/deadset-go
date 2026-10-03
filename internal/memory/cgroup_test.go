package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// machine writes files under a fresh root, each path relative to it.
func machine(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("Setup: create %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", path, err)
		}
	}
	return root
}

const (
	gib     = uint64(1) << 30
	mib     = uint64(1) << 20
	meminfo = "MemTotal:       32000000 kB\nMemFree:         1000000 kB\nMemAvailable:   16000000 kB\n"
)

// v2Mountinfo mounts cgroup v2 at /sys/fs/cgroup with the given root.
func v2Mountinfo(root string) string {
	return "22 1 0:21 / / rw - ext4 /dev/sda1 rw\n" +
		"30 22 0:26 " + root + " /sys/fs/cgroup rw,nosuid - cgroup2 cgroup2 rw\n"
}

// headroomOf reads one source of linuxSources over root.
func headroomOf(t *testing.T, root string, source int) (uint64, bool) {
	t.Helper()
	return linuxSources(root)[source]()
}

const (
	sourceV2 = iota
	sourceV1
	sourceMeminfo
)

func TestCgroupV2ReadsTheTightestLimitOfEveryAncestor(t *testing.T) {
	root := machine(t, map[string]string{
		"proc/self/mountinfo":                         v2Mountinfo("/"),
		"proc/self/cgroup":                            "0::/user.slice/job\n",
		"sys/fs/cgroup/user.slice/memory.max":         "4294967296\n",
		"sys/fs/cgroup/user.slice/memory.high":        "max\n",
		"sys/fs/cgroup/user.slice/memory.current":     "1073741824\n",
		"sys/fs/cgroup/user.slice/memory.stat":        "anon 1\ninactive_file 0\n",
		"sys/fs/cgroup/user.slice/job/memory.max":     "max\n",
		"sys/fs/cgroup/user.slice/job/memory.high":    "max\n",
		"sys/fs/cgroup/user.slice/job/memory.current": "536870912\n",
		"sys/fs/cgroup/user.slice/job/memory.stat":    "inactive_file 0\n",
		"sys/fs/cgroup/memory.current":                "8589934592\n",
	})
	got, ok := headroomOf(t, root, sourceV2)
	if want := 3 * gib; !ok || got != want {
		t.Errorf("cgroup v2 headroom with a 4 GiB parent limit holding 1 GiB = %d, %v; want %d, true", got, ok, want)
	}
}

func TestCgroupV2CountsInactiveFileCacheAsRoom(t *testing.T) {
	root := machine(t, map[string]string{
		"proc/self/mountinfo":          v2Mountinfo("/"),
		"proc/self/cgroup":             "0::/\n",
		"sys/fs/cgroup/memory.max":     "7516192768\n",
		"sys/fs/cgroup/memory.high":    "6442450944\n",
		"sys/fs/cgroup/memory.current": "6174015488\n",
		"sys/fs/cgroup/memory.stat":    "anon 805306368\ninactive_file 5368709120\nactive_file 0\n",
	})
	got, ok := headroomOf(t, root, sourceV2)
	// memory.high is the tighter limit: 6 GiB less a working set of 5.75 GiB used
	// minus 5 GiB of inactive cache.
	if want := 6*gib - (5888*mib - 5*gib); !ok || got != want {
		t.Errorf("cgroup v2 headroom with 5 GiB of inactive file cache = %d, %v; want %d, true", got, ok, want)
	}
}

func TestCgroupV2ResolvesAContainerMountedAtItsOwnCgroup(t *testing.T) {
	root := machine(t, map[string]string{
		"proc/self/mountinfo":          v2Mountinfo("/docker/abc"),
		"proc/self/cgroup":             "0::/docker/abc\n",
		"sys/fs/cgroup/memory.max":     "2147483648\n",
		"sys/fs/cgroup/memory.current": "1073741824\n",
		"sys/fs/cgroup/memory.stat":    "inactive_file 0\n",
	})
	got, ok := headroomOf(t, root, sourceV2)
	if want := gib; !ok || got != want {
		t.Errorf("cgroup v2 headroom in a container whose mount root is its cgroup = %d, %v; want %d, true", got, ok, want)
	}
}

func TestCgroupV2AnswersNothingWhereEveryLimitIsMax(t *testing.T) {
	root := machine(t, map[string]string{
		"proc/self/mountinfo":          v2Mountinfo("/"),
		"proc/self/cgroup":             "0::/\n",
		"sys/fs/cgroup/memory.max":     "max\n",
		"sys/fs/cgroup/memory.high":    "max\n",
		"sys/fs/cgroup/memory.current": "4981530624\n",
		"sys/fs/cgroup/memory.stat":    "inactive_file 433274880\n",
	})
	if got, ok := headroomOf(t, root, sourceV2); ok {
		t.Errorf("cgroup v2 headroom with no limit set = %d, true; want false", got)
	}
}

func TestCgroupV1ReadsTheHierarchicalLimitThroughMountinfo(t *testing.T) {
	root := machine(t, map[string]string{
		"proc/self/mountinfo": "22 1 0:21 / / rw - ext4 /dev/sda1 rw\n" +
			"32 22 0:28 /docker/abc /sys/fs/cgroup/cpu rw,nosuid - cgroup cgroup rw,cpu,cpuacct\n" +
			"31 22 0:27 /docker/abc /sys/fs/cgroup/memory rw,nosuid - cgroup cgroup rw,memory\n",
		"proc/self/cgroup": "12:memory:/docker/abc/job\n11:cpu,cpuacct:/docker/abc/other\n",
		"sys/fs/cgroup/memory/job/memory.limit_in_bytes": "9223372036854771712\n",
		"sys/fs/cgroup/memory/job/memory.usage_in_bytes": "3221225472\n",
		"sys/fs/cgroup/memory/job/memory.stat":           "cache 0\nhierarchical_memory_limit 4294967296\ntotal_inactive_file 1073741824\n",
	})
	got, ok := headroomOf(t, root, sourceV1)
	if want := 2 * gib; !ok || got != want {
		t.Errorf("cgroup v1 headroom under a 4 GiB parent limit with 1 GiB of inactive cache = %d, %v; want %d, true", got, ok, want)
	}
}

func TestCgroupV1AnswersNothingUnderTheUnlimitedSentinel(t *testing.T) {
	root := machine(t, map[string]string{
		"proc/self/mountinfo":                        "31 22 0:27 / /sys/fs/cgroup/memory rw - cgroup cgroup rw,memory\n",
		"proc/self/cgroup":                           "12:memory:/\n",
		"sys/fs/cgroup/memory/memory.usage_in_bytes": "3221225472\n",
		"sys/fs/cgroup/memory/memory.stat":           "hierarchical_memory_limit 9223372036854771712\n",
	})
	if got, ok := headroomOf(t, root, sourceV1); ok {
		t.Errorf("cgroup v1 headroom with no limit set = %d, true; want false", got)
	}
}

func TestMemAvailableIsReadInBytes(t *testing.T) {
	root := machine(t, map[string]string{"proc/meminfo": meminfo})
	got, ok := headroomOf(t, root, sourceMeminfo)
	if want := uint64(16000000) << 10; !ok || got != want {
		t.Errorf("MemAvailable headroom = %d, %v; want %d, true", got, ok, want)
	}
}

func TestNoSourceAnswersOnAMachineWithoutProc(t *testing.T) {
	root := machine(t, map[string]string{"etc/hostname": "box\n"})
	for i, source := range linuxSources(root) {
		if got, ok := source(); ok {
			t.Errorf("source %d on a machine without /proc = %d, true; want false", i, got)
		}
	}
}

func TestMountinfoUnescapesTheMountPoint(t *testing.T) {
	v2, _ := mountsOf([]byte(`30 22 0:26 / /sys/fs/my\040cgroup rw - cgroup2 cgroup2 rw` + "\n"))
	if v2 == nil || v2.point != "/sys/fs/my cgroup" {
		t.Errorf("mountsOf(an escaped space) = %+v, want the point /sys/fs/my cgroup", v2)
	}
}
