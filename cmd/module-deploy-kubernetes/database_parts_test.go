package main

import (
	"strings"
	"testing"
)

// The core describes a database as its parts and this module assembles them. A password with an
// `@` in it is legal in a database, so it must not be going through URL escaping — this test
// exists because getting that wrong produces "password authentication failed" on the one
// database whose whole purpose is to be reached.
func TestDatabasePartsAssemblesADSNThatSurvivesAwkwardPasswords(t *testing.T) {
	parts := &databaseParts{Kind: "db", Name: "dogit_deploy"}
	parts.Payload.Host = "postgres"
	parts.Payload.Port = "5432"
	parts.Payload.Database = "dogit_deploy"
	parts.Payload.User = "dogit_deploy"
	parts.Payload.Password = "p@ss:w/rd with spaces"

	dsn, err := parts.connectionString()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"host=postgres", "port=5432", "dbname=dogit_deploy", "user=dogit_deploy",
		"password=p@ss:w/rd with spaces",
	} {
		if !strings.Contains(dsn, want) {
			t.Errorf("%q is missing from %q", want, dsn)
		}
	}
}

// A part left out is named. Without this the module would hand its driver a DSN missing a field
// and the driver would refuse a socket, which says nothing about the field that was absent.
func TestDatabasePartsNamesEveryPartItIsMissing(t *testing.T) {
	parts := &databaseParts{}
	_, err := parts.connectionString()
	if err == nil {
		t.Fatal("a database with no parts at all was assembled into a connection string")
	}
	for _, want := range []string{"host", "port", "database", "user", "password"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q is not named in %v", want, err)
		}
	}
}

// One part missing is one part missing. Saying all five were absent when one was would send
// somebody looking for four problems that are not there.
func TestDatabasePartsNamesOnlyWhatIsActuallyMissing(t *testing.T) {
	parts := &databaseParts{}
	parts.Payload.Host = "postgres"
	parts.Payload.Port = "5432"
	parts.Payload.Database = "dogit_deploy"
	parts.Payload.User = "dogit_deploy"

	_, err := parts.connectionString()
	if err == nil {
		t.Fatal("a database with no password was assembled")
	}
	// Matched as a list rather than as substrings: "database" is a substring of the sentence
	// saying a *database* was described, so a substring test would report a false failure and
	// hide the one that matters.
	named := missingParts(t, err)
	if len(named) != 1 || named[0] != "password" {
		t.Errorf("wanted only the password named as missing, got %v", named)
	}
}

// missingParts reads back which parts an error named, from the only place they are listed.
//
// Taken as the comma-separated run after "no", because the sentence around it mentions every
// part by name on purpose — a reader who cannot see the message in full still wants to know
// which fields to go and fill in.
func missingParts(t *testing.T, err error) []string {
	t.Helper()
	_, after, found := strings.Cut(err.Error(), " with no ")
	if !found {
		t.Fatalf("the error does not list what is missing: %v", err)
	}
	list, _, _ := strings.Cut(after, ",")
	if list == "" {
		list = after
	}
	// The list ends at the comma before the explanation.
	if cut, _, ok := strings.Cut(list, ", and"); ok {
		list = cut
	}
	// Everything after the first semicolon is the explanation of why this matters, not a field.
	fields, _, _ := strings.Cut(list, ";")
	out := []string{}
	for _, name := range strings.Split(fields, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// A blank value is an absent one. A core that sent `"host": "  "` has told this module nothing,
// and treating it as told something produces a connection to a host named by two spaces.
func TestDatabasePartsTreatsBlankAsMissing(t *testing.T) {
	parts := &databaseParts{}
	parts.Payload.Host = "   "
	parts.Payload.Port = "5432"
	parts.Payload.Database = "dogit_deploy"
	parts.Payload.User = "dogit_deploy"
	parts.Payload.Password = "secret"

	if _, err := parts.connectionString(); err == nil {
		t.Fatal("a host of three spaces was accepted as a host")
	}
}
