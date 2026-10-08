package store

import (
	_ "embed"
	"regexp"
	"strings"
	"testing"
)

// The column list and the destinations of a Scan are written by hand, three times over, and
// nothing makes them agree.
//
// That has gone wrong twice, and both times the failure looked like something else: a column
// left out of the list makes a row read back short, which reads as a value nobody wrote down,
// and a column left out of the destinations makes the driver refuse the query, which reads as a
// database problem. Neither is a database problem, and neither was caught until a test against a
// real database happened to read this table.
//
// So the two are compared here, which needs no database and fails immediately rather than on
// somebody's request.
func TestEveryResourceColumnIsScannedIntoSomething(t *testing.T) {
	columns := strings.Split(resourceColumns, ",")
	expected := 0
	for _, column := range columns {
		if strings.TrimSpace(column) != "" {
			expected++
		}
	}

	for _, scan := range []struct {
		name  string
		body  string
		extra int // columns joined in by the query, beyond the shared list
	}{
		{name: "scanResource", body: scanBody(t, "row.Scan"), extra: 0},
		{name: "List", body: scanBody(t, "rows.Scan"), extra: 2},
	} {
		destinations := 0
		for _, destination := range strings.Split(scan.body, ",") {
			if strings.TrimSpace(destination) != "" {
				destinations++
			}
		}
		if want := expected + scan.extra; destinations != want {
			t.Errorf("%s scans %d values but the query selects %d: a column is in one list "+
				"and not the other, which reads as a value nobody wrote down or as a driver "+
				"refusing the query", scan.name, destinations, want)
		}
	}
}

// The two RETURNING lists are the same columns written out again, and were the first half of
// this: a column added to one and forgotten in the other is a row that reads back short.
func TestBothReturningListsHaveEveryColumn(t *testing.T) {
	expected := 0
	for _, column := range strings.Split(resourceColumns, ",") {
		if strings.TrimSpace(column) != "" {
			expected++
		}
	}
	for name, list := range map[string]string{
		"returnColumnsPlain":  returnColumnsPlain,
		"returnColumnsJoined": returnColumnsJoined,
	} {
		count := 0
		for _, column := range strings.Split(list, ",") {
			if strings.TrimSpace(column) != "" {
				count++
			}
		}
		if count != expected {
			t.Errorf("%s names %d columns and resourceColumns names %d", name, count, expected)
		}
	}
}

// The joined form has to stay qualified and the plain one un-qualified: Postgres refuses a
// qualified SET target and equally refuses an unqualified RETURNING column when a FROM clause
// brings in a table of the same name. Getting this wrong is a syntax or ambiguity error at the
// moment a module is removed.
func TestTheJoinedReturningListIsQualifiedAndThePlainOneIsNot(t *testing.T) {
	if !strings.Contains(returnColumnsJoined, "resources.id") {
		t.Error("the joined list must qualify its columns, or an UPDATE ... FROM reads them as " +
			"ambiguous between the table being written and the one joined in")
	}
	if strings.Contains(returnColumnsPlain, "resources.") {
		t.Error("the plain list must not qualify its columns: in an UPDATE with no FROM, " +
			"Postgres reports a qualified column as one that does not exist")
	}
}

var scanCall = regexp.MustCompile(`(?s)(row|rows)\.Scan\((.*?)\)`)

// thisFile is resources.go, embedded so that this test can read the queries it is checking
// rather than a second copy of them. A copy would be a third place to forget.
var thisFile []byte

//go:embed resources.go
var embeddedResources []byte

func init() { thisFile = embeddedResources }

// scanBody pulls out the destinations of the first call to the named Scan, so that the test
// does not keep its own copy of them.
func scanBody(t *testing.T, call string) string {
	t.Helper()
	for _, match := range scanCall.FindAllStringSubmatch(string(thisFile), -1) {
		if match[1] == strings.TrimSuffix(call, ".Scan") {
			return match[2]
		}
	}
	t.Fatalf("%s is not called in resources.go, so this test is checking nothing", call)
	return ""
}
