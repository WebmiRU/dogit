// Package measure is this process reading itself and the machine it is on.
//
// Every reading here can come back "not measured", and that is the point of the shape. The
// panel this feeds has a fixed set of boxes, and a box that says "not reported" tells a person
// something true — that the module did not look — where a zero would tell them something
// false and reassuring. A container that cannot see the node's memory, a field that was never
// read, a moment before the first sample: all of them are nil, and all of them are better
// than a number nobody measured.
package measure

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// clockTicks is the kernel's USER_HZ. It is 100 on every Linux this runs on and Go exposes no
// way to ask, so it is written down rather than guessed at each use.
const clockTicks = 100

// Self is what this process is using.
//
// CPU is a rate, so the first call has nothing to compare against and says so. Sending a zero
// instead would put a flat line on a graph in the admin page, and a flat line is read as
// "this module does nothing" rather than as "this module has been up for ten seconds".
type Self struct {
	previousCPU    time.Duration
	previousSample time.Time
	previousRSS    int64
}

// ProcessCPUPercent is this process's CPU use since the last call, or nil on the first one.
func (s *Self) ProcessCPUPercent() *float64 {
	stat, ok := readStat()
	if !ok {
		return nil
	}
	now := time.Now()
	cpu := time.Duration(stat.utime+stat.stime) * time.Second / clockTicks

	percent := func() *float64 {
		elapsed := now.Sub(s.previousSample)
		if elapsed <= 0 {
			return nil
		}
		used := float64(cpu-s.previousCPU) / float64(elapsed) * 100
		if used < 0 {
			// A negative rate is a clock that went backwards, not a process that used less
			// than nothing. Reported as not measured rather than as a number.
			return nil
		}
		value := used / float64(runtime.NumCPU())
		return &value
	}()

	s.previousCPU, s.previousSample = cpu, now
	return percent
}

// ProcessMemoryBytes is this process's resident memory.
func (s *Self) ProcessMemoryBytes() *int64 {
	stat, ok := readStat()
	if !ok {
		return nil
	}
	s.previousRSS = stat.rss * int64(os.Getpagesize())
	return &s.previousRSS
}

// Host is the machine, as far as this process can see it.
//
// A container without its own PID namespace reads the host's /proc, so these are the node's
// numbers rather than the pod's. That is what the panel's "Node" box is for, and the pod's own
// ceiling is reported separately, because a pod limited to 512 MB on a 4 GB node is a fact the
// node's 4 GB does not contain.
type Host struct {
	MemoryTotalBytes *int64
	MemoryUsedBytes  *int64
	Load1            *float64
	Cores            *int64
	CgroupLimitBytes *int64
}

// ReadHost takes one reading of the machine.
func ReadHost() Host {
	host := Host{Cores: pointer(int64(runtime.NumCPU()))}

	if total, available, ok := readMeminfo(); ok {
		host.MemoryTotalBytes = &total
		used := total - available
		host.MemoryUsedBytes = &used
	}
	if load, ok := readLoad(); ok {
		host.Load1 = &load
	}
	if limit, ok := readCgroupMemoryLimit(); ok {
		host.CgroupLimitBytes = &limit
	}
	return host
}

// Disk is a filesystem's size, or nil when the path cannot be read at all.
type Disk struct {
	TotalBytes *int64
	UsedBytes  *int64
}

// ReadDisk measures the filesystem holding one path.
//
// "Cannot be measured" is separated from "measured as zero" on purpose. A path that does not
// exist yet — the workspace, before anything has been checked out into it — reads as absent,
// and the panel should say that rather than draw a full disk or an empty one.
func ReadDisk(path string) Disk {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return Disk{}
	}
	blockSize := int64(stat.Bsize)
	total := int64(stat.Blocks) * blockSize
	// Bavail rather than Bfree: the blocks held by root's reserved percentage are not
	// available to this process and must not be counted as space this runner could use.
	free := int64(stat.Bavail) * blockSize
	used := total - free
	return Disk{TotalBytes: &total, UsedBytes: &used}
}

type procStat struct {
	utime, stime uint64
	rss          int64
}

func readStat() (procStat, bool) {
	raw, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return procStat{}, false
	}
	// The second field is the command in parentheses and may itself contain spaces and
	// parentheses, so the fields after it are found from the last ")" rather than by
	// counting from the start. Counting from the start is a bug that waits for somebody to
	// name their process "go test (x)".
	fields := fieldsAfterCommand(string(raw))
	if fields == nil {
		return procStat{}, false
	}
	utime, err1 := strconv.ParseUint(fields[11], 10, 64)
	stime, err2 := strconv.ParseUint(fields[12], 10, 64)
	rss, err3 := strconv.ParseInt(fields[21], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return procStat{}, false
	}
	return procStat{utime: utime, stime: stime, rss: rss}, true
}

// fieldsAfterCommand is everything in a /proc stat line that follows the command in
// parentheses, or nil when there is nothing usable there.
//
// After the closing parenthesis the remainder starts at the state field, which was field 3 of
// the line, so utime and stime sit at 11 and 12 here and rss at 21 — field 24 overall.
func fieldsAfterCommand(line string) []string {
	close := strings.LastIndex(line, ")")
	if close < 0 {
		return nil
	}
	fields := strings.Fields(line[close+1:])
	if len(fields) < 22 {
		return nil
	}
	return fields
}

func readMeminfo() (total, available int64, ok bool) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	found := 0
	for _, line := range strings.Split(string(raw), "\n") {
		name, value, found2 := strings.Cut(line, ":")
		if !found2 {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		kilobytes, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch name {
		case "MemTotal":
			total, found = kilobytes*1024, found|1
		case "MemAvailable":
			available, found = kilobytes*1024, found|2
		}
	}
	return total, available, found == 3
}

func readLoad() (float64, bool) {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0, false
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// readCgroupMemoryLimit is the pod's own ceiling, on cgroup v2 and then on v1.
//
// "max" is what an unlimited cgroup says, and it is not a number. It comes back as absent
// rather than as the host's memory, because a container told it may use everything has not
// measured how much everything is.
func readCgroupMemoryLimit() (int64, bool) {
	for _, path := range []string{
		"/sys/fs/cgroup/memory.max",
		"/sys/fs/cgroup/memory/memory.limit_in_bytes",
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(raw))
		if text == "max" {
			return 0, false
		}
		value, err := strconv.ParseInt(text, 10, 64)
		if err != nil || value <= 0 {
			// A very large number is the cgroup v1 way of saying "no limit": it is the
			// page size times a huge multiplier, and treating it as a real number would put
			// an absurd ceiling in the panel.
			continue
		}
		return value, true
	}
	return 0, false
}

func pointer[T any](value T) *T { return &value }
