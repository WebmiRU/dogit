package measure

import (
	"os"
	"strings"
	"testing"
)

// The second field of /proc/self/stat is the command in parentheses and may contain spaces
// and parentheses of its own, so the fields after it are found from the last ")". Counting
// from the start of the line is a bug that waits for somebody to name a process
// "go test (something)".
func TestStatWithAwkwardCommandName(t *testing.T) {
	// Built from the offsets rather than typed out, so that the data and the expectation
	// cannot disagree — which is exactly how the first version of this test failed, with a
	// hand-written line whose numbers sat one field to the left of what it asserted.
	const (
		afterCommand = 11 // utime, overall field 14
		afterStime   = 12 // stime, overall field 15
		afterRSS     = 21 // rss, overall field 24
	)
	line := "4242 (go test (x) y)" + statTail(map[int]string{
		afterCommand: "11",
		afterStime:   "22",
		afterRSS:     "12345",
	})

	fields := fieldsAfterCommand(line)
	if fields == nil {
		t.Fatal("fieldsAfterCommand returned nil for a well-formed line")
	}
	for index, want := range map[int]string{
		afterCommand: "11", afterStime: "22", afterRSS: "12345",
	} {
		if got := fields[index]; got != want {
			t.Errorf("field %d is %q, want %q", index, got, want)
		}
	}
}

func TestStatWithoutAClosingParenthesis(t *testing.T) {
	if got := fieldsAfterCommand("no parentheses here at all"); got != nil {
		t.Fatalf("got %v, want nil for a line with no command field", got)
	}
}

func TestStatTooShort(t *testing.T) {
	// A slice short enough to index past its end is a panic, and a panic in a heartbeat is a
	// module that stops reporting and takes its queue with it.
	if got := fieldsAfterCommand("1 (x) S 1 2 3"); got != nil {
		t.Fatalf("got %v, want nil rather than a short slice", got)
	}
}

// Against this process, which is the one line of /proc the runner will actually read.
func TestStatOnThisProcess(t *testing.T) {
	stat, ok := readStat()
	if !ok {
		t.Skip("no /proc/self/stat here")
	}
	if stat.rss <= 0 {
		t.Errorf("rss is %d, want a positive number for a running process", stat.rss)
	}
	// utime and stime are left alone on purpose. A tick is ten milliseconds, so a process
	// that has just started legitimately reports zero for both, and a test that demanded a
	// positive number would be asserting that the test binary had been running for a tenth of
	// a second — which is a thing to be lucky with, not to require.
}

// ReadDisk on a path that cannot exist, and on one that can. A missing path must come back
// unmeasured: it is the workspace before the first checkout, and reporting it as an empty disk
// would draw a full bar in the admin page for a filesystem nobody has written to.
func TestReadDisk(t *testing.T) {
	if disk := ReadDisk("/definitely/not/here/at/all"); disk.TotalBytes != nil {
		t.Errorf("got %v for a path that does not exist, want unmeasured", disk)
	}

	disk := ReadDisk(os.TempDir())
	if disk.TotalBytes == nil || *disk.TotalBytes <= 0 {
		t.Fatalf("got %v for %s, want a positive total", disk, os.TempDir())
	}
	if disk.UsedBytes == nil || *disk.UsedBytes < 0 {
		t.Errorf("got %v, want a used figure that is not negative", disk.UsedBytes)
	}
}

// statTail builds the part of a /proc stat line that follows the command, with the named
// offsets filled in and a placeholder everywhere else.
//
// The placeholder is "0" and not "": an empty slot joins into a run of spaces, and the parser
// splits on whitespace, so the empties vanish and the line arrives with four fields instead
// of twenty-two. That is not a hypothetical — it is what the first version of this helper did,
// and the test written against it got past a nil check for a reason that had nothing to do
// with what it meant to check.
func statTail(values map[int]string) string {
	fields := make([]string, 22)
	for index := range fields {
		fields[index] = "0"
	}
	fields[0] = "S" // state, overall field 3
	for index, value := range values {
		fields[index] = value
	}
	return " " + strings.Join(fields, " ")
}
