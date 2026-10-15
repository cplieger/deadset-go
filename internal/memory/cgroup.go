package memory

import (
	"bufio"
	"bytes"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// unlimitedV1 is the smallest value cgroup v1 reports for a memory limit nobody
// set: the largest page-aligned 64-bit value, which no machine holds.
const unlimitedV1 = 1 << 62

// mount is one cgroup filesystem mounted in this process's view: the cgroup path
// its root is, and where it is mounted.
type mount struct {
	root  string
	point string
}

// mountsOf reads the cgroup v2 mount and the cgroup v1 memory mount from a
// mountinfo document. A field after the separator names the filesystem type, and
// for v1 the super options name the controllers.
func mountsOf(mountinfo []byte) (v2, v1 *mount) {
	scanner := bufio.NewScanner(bytes.NewReader(mountinfo))
	for scanner.Scan() {
		before, after, found := strings.Cut(scanner.Text(), " - ")
		fields, tail := strings.Fields(before), strings.Fields(after)
		if !found || len(fields) < 5 || len(tail) < 1 {
			continue
		}
		at := &mount{root: fields[3], point: unescape(fields[4])}
		switch {
		case tail[0] == "cgroup2" && v2 == nil:
			v2 = at
		case tail[0] == "cgroup" && len(tail) >= 3 && hasOption(tail[2], "memory") && v1 == nil:
			v1 = at
		}
	}
	return v2, v1
}

// hasOption reports whether a comma-separated option list names one option.
func hasOption(options, name string) bool {
	for one := range strings.SplitSeq(options, ",") {
		if one == name {
			return true
		}
	}
	return false
}

// unescape decodes the octal escapes mountinfo writes for a space, a tab, a line
// feed and a backslash in a path.
func unescape(field string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(field)
}

// cgroupPaths reads the process's cgroup v2 path and its cgroup v1 memory path
// from a /proc/self/cgroup document.
func cgroupPaths(document []byte) (v2, v1 string) {
	scanner := bufio.NewScanner(bytes.NewReader(document))
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), ":", 3)
		if len(parts) != 3 {
			continue
		}
		switch {
		case parts[0] == "0" && parts[1] == "":
			v2 = parts[2]
		case hasOption(parts[1], "memory"):
			v1 = parts[2]
		}
	}
	return v2, v1
}

// directoryOf is where one cgroup path is in this process's view of a mount: the
// path below the mount's root, joined to the mount point. A path outside the
// mount's root is the root itself, which is what a container mounted at its own
// cgroup sees.
func directoryOf(at *mount, cgroup string) string {
	root, group := path.Clean(at.root), path.Clean(cgroup)
	below := ""
	switch {
	case root == "/":
		below = group
	case group == root || strings.HasPrefix(group, root+"/"):
		below = strings.TrimPrefix(group, root)
	}
	return filepath.Join(at.point, filepath.FromSlash(below))
}

// headroomV2 is the least headroom any cgroup v2 directory from leaf up to the
// mount point leaves: its tightest limit, memory.max or memory.high, minus its
// working set, memory.current less the inactive file cache the kernel reclaims
// before it kills.
func headroomV2(point, leaf string) (uint64, bool) {
	var least uint64
	found := false
	for _, dir := range ancestry(point, leaf) {
		if limit, limited := tightestV2(dir); limited {
			if room, read := roomUnder(limit, filepath.Join(dir, "memory.current"), filepath.Join(dir, "memory.stat"), "inactive_file"); read && (!found || room < least) {
				least, found = room, true
			}
		}
	}
	return least, found
}

// limitV2 is the lowest memory.max of any cgroup v2 directory from leaf up to the
// mount point, and false where none sets one.
func limitV2(point, leaf string) (uint64, bool) {
	var least uint64
	found := false
	for _, dir := range ancestry(point, leaf) {
		if limit, set := readUint(filepath.Join(dir, "memory.max")); set && (!found || limit < least) {
			least, found = limit, true
		}
	}
	return least, found
}

// ancestry is leaf and each directory above it up to the mount point.
func ancestry(point, leaf string) []string {
	var dirs []string
	for dir := leaf; ; dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
		if dir == point || !strings.HasPrefix(dir, point) || dir == filepath.Dir(dir) {
			return dirs
		}
	}
}

// tightestV2 is the lower of the two limits one cgroup v2 directory sets, and
// false where it sets neither.
func tightestV2(dir string) (uint64, bool) {
	var limit uint64
	limited := false
	for _, name := range []string{"memory.max", "memory.high"} {
		if value, set := readUint(filepath.Join(dir, name)); set && (!limited || value < limit) {
			limit, limited = value, true
		}
	}
	return limit, limited
}

// headroomV1 is the headroom one cgroup v1 memory directory leaves: the
// hierarchical limit memory.stat reports, which is the tightest of the directory
// and its ancestors, minus the usage less the inactive file cache.
func headroomV1(dir string) (uint64, bool) {
	limit, set := statField(filepath.Join(dir, "memory.stat"), "hierarchical_memory_limit")
	if !set || limit >= unlimitedV1 {
		return 0, false
	}
	return roomUnder(limit, filepath.Join(dir, "memory.usage_in_bytes"), filepath.Join(dir, "memory.stat"), "total_inactive_file")
}

// limitV1 is the memory limit of one cgroup v1 memory directory: the lower of its
// own memory.limit_in_bytes and the hierarchical limit its ancestors set, and
// false where neither is set.
func limitV1(dir string) (uint64, bool) {
	limit, set := readUint(filepath.Join(dir, "memory.limit_in_bytes"))
	if hierarchical, read := statField(filepath.Join(dir, "memory.stat"), "hierarchical_memory_limit"); read && (!set || hierarchical < limit) {
		limit, set = hierarchical, true
	}
	if !set || limit >= unlimitedV1 {
		return 0, false
	}
	return limit, true
}

// roomUnder is limit minus the working set the usage file and the stat field give.
func roomUnder(limit uint64, usageFile, statFile, inactiveField string) (uint64, bool) {
	usage, read := readUint(usageFile)
	if !read {
		return 0, false
	}
	inactive, _ := statField(statFile, inactiveField)
	working := usage - min(inactive, usage)
	if working >= limit {
		return 0, true
	}
	return limit - working, true
}

// readUint reads one file holding one unsigned integer, and false where it holds
// anything else, which is how a cgroup v2 limit of max reads as no limit.
func readUint(file string) (uint64, bool) {
	body, err := os.ReadFile(file)
	if err != nil {
		return 0, false
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(body)), 10, 64)
	return value, err == nil
}

// statField reads one field of a memory.stat file.
func statField(file, field string) (uint64, bool) {
	body, err := os.ReadFile(file)
	if err != nil {
		return 0, false
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		name, value, found := strings.Cut(scanner.Text(), " ")
		if found && name == field {
			parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
			return parsed, err == nil
		}
	}
	return 0, false
}

// memAvailable reads MemAvailable from a meminfo document, in bytes.
func memAvailable(meminfo []byte) (uint64, bool) {
	return meminfoField(meminfo, "MemAvailable:")
}

// meminfoField reads one field of a meminfo document, in bytes.
func meminfoField(meminfo []byte, field string) (uint64, bool) {
	scanner := bufio.NewScanner(bytes.NewReader(meminfo))
	for scanner.Scan() {
		rest, found := strings.CutPrefix(scanner.Text(), field)
		if !found {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0, false
		}
		kibibytes, err := strconv.ParseUint(fields[0], 10, 64)
		return kibibytes << 10, err == nil
	}
	return 0, false
}

// linux is the Linux machine over a filesystem rooted at root: its readers are
// cgroup v2, cgroup v1 and MemAvailable, each answering where its files say a bound
// applies.
func linux(root string) Machine {
	fs := procfs{root: root}
	return Machine{Sources: []Source{fs.headroomV2, fs.headroomV1, fs.memAvailable}, Limit: fs.limit}
}

// procfs reads the files of a Linux filesystem rooted at root.
type procfs struct{ root string }

// read is one file's body, nil where it does not read.
func (p procfs) read(name string) []byte {
	body, err := os.ReadFile(filepath.Join(p.root, name))
	if err != nil {
		return nil
	}
	return body
}

// v2Dir is the cgroup v2 mount point and the process's directory below it.
func (p procfs) v2Dir() (point, leaf string, ok bool) {
	v2, _ := mountsOf(p.read("proc/self/mountinfo"))
	group, _ := cgroupPaths(p.read("proc/self/cgroup"))
	if v2 == nil || group == "" {
		return "", "", false
	}
	point = filepath.Join(p.root, v2.point)
	return point, directoryOf(&mount{root: v2.root, point: point}, group), true
}

// v1Dir is the process's cgroup v1 memory directory.
func (p procfs) v1Dir() (string, bool) {
	_, v1 := mountsOf(p.read("proc/self/mountinfo"))
	_, group := cgroupPaths(p.read("proc/self/cgroup"))
	if v1 == nil || group == "" {
		return "", false
	}
	return directoryOf(&mount{root: v1.root, point: filepath.Join(p.root, v1.point)}, group), true
}

func (p procfs) headroomV2() (uint64, bool) {
	point, leaf, ok := p.v2Dir()
	if !ok {
		return 0, false
	}
	return headroomV2(point, leaf)
}

func (p procfs) headroomV1() (uint64, bool) {
	dir, ok := p.v1Dir()
	if !ok {
		return 0, false
	}
	return headroomV1(dir)
}

func (p procfs) memAvailable() (uint64, bool) { return memAvailable(p.read("proc/meminfo")) }

// limit is the tightest cgroup memory limit, v2 first, capped by MemTotal, else
// MemTotal.
func (p procfs) limit() (uint64, bool) {
	cgroup, limited := uint64(0), false
	if point, leaf, ok := p.v2Dir(); ok {
		cgroup, limited = limitV2(point, leaf)
	}
	if dir, ok := p.v1Dir(); ok && !limited {
		cgroup, limited = limitV1(dir)
	}
	physical, known := meminfoField(p.read("proc/meminfo"), "MemTotal:")
	return tighter(cgroup, limited, physical, known)
}

// tighter is the lower of a cgroup's limit and the physical memory, each where it
// reads, and false where neither does.
func tighter(cgroup uint64, limited bool, physical uint64, known bool) (uint64, bool) {
	switch {
	case limited && known:
		return min(cgroup, physical), true
	case limited:
		return cgroup, true
	}
	return physical, known
}
