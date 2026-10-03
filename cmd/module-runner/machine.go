package main

import (
	"context"
	"os"
	"strings"
	"syscall"
)

// machine reads what the operating system will say about itself.
//
// Everything here can fail — a field the kernel does not expose, a value that is
// not there to read — and every failure leaves the number out rather than putting a
// zero there. A runner reporting "this machine has no memory" would be believed by
// an administrator deciding whether the disk is the problem.
func machine() (cpus float64, memory int64, ok bool) {
	cpus = float64(countCPUs())
	memory = readMemTotal()
	return cpus, memory, cpus > 0 || memory > 0
}

func countCPUs() int {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		// How many processors this process may use is the next best thing to how many
		// the machine has, and it is always readable.
		return len(schedGetaffinity())
	}
	return strings.Count(string(data), "processor\t:")
}

// memoryTotal reads MemTotal from /proc/meminfo, which is where Linux states it.
func readMemTotal() int64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		value := parseInt64(fields[1])
		return value * 1024
	}
	return 0
}

// diskFree reports the filesystem behind a directory.
func diskFree(dir string) (total, free int64, ok bool) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, 0, false
	}
	total = int64(stat.Blocks) * int64(stat.Bsize)
	free = int64(stat.Bavail) * int64(stat.Bsize)
	return total, free, true
}

// dockerVersion is what this machine's Docker says it is.
//
// A runner that cannot say is a runner whose builds are going to fail in ways that
// are hard to read, so it is worth a line in the module's reported statistics.
func dockerVersion() string {
	binary := os.Getenv("DOGIT_DOCKER_BINARY")
	if binary == "" {
		binary = "docker"
	}

	output, err := run(context.Background(), binary, "version", "--format", "{{.Server.Version}}")
	if err != nil {
		// Not an error worth stopping for: a machine whose Docker is starting up
		// simply reports nothing yet.
		return ""
	}
	return strings.TrimSpace(output)
}

// schedGetaffinity reports the processors this process may run on, which is what
// a container is limited to.
func schedGetaffinity() []int {
	var cpus []int
	if err := schedGetaffinityInto(&cpus); err != nil {
		return nil
	}
	return cpus
}

func parseInt64(value string) int64 {
	parsed := int64(0)
	negative := false
	for index, char := range value {
		if index == 0 && char == '-' {
			negative = true
			continue
		}
		if char < '0' || char > '9' {
			return 0
		}
		parsed = parsed*10 + int64(char-'0')
	}
	if negative {
		return -parsed
	}
	return parsed
}
