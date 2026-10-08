package main

import (
	"strings"
	"testing"
)

// The core describes a resource as its parts and this module assembles a DSN from them. A password with an
// `@` in it is legal in a database, so it must not be going through URL escaping — this test
// exists because getting that wrong produces "password authentication failed" on the one
// database whose whole purpose is to be reached.
func TestResourceHandoverAssemblesADSNThatSurvivesAwkwardPasswords(t *testing.T) {
	parts := &resourceHandover{Kind: "db", Name: "dogit_deploy"}
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
func TestResourceHandoverNamesEveryPartItIsMissing(t *testing.T) {
	parts := &resourceHandover{}
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
func TestResourceHandoverNamesOnlyWhatIsActuallyMissing(t *testing.T) {
	parts := &resourceHandover{}
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
func TestResourceHandoverTreatsBlankAsMissing(t *testing.T) {
	parts := &resourceHandover{}
	parts.Payload.Host = "   "
	parts.Payload.Port = "5432"
	parts.Payload.Database = "dogit_deploy"
	parts.Payload.User = "dogit_deploy"
	parts.Payload.Password = "secret"

	if _, err := parts.connectionString(); err == nil {
		t.Fatal("a host of three spaces was accepted as a host")
	}
}

// The core handed over no database at all. This module still deploys without one and records
// nothing, which from the outside is a module that has never deployed anything — the exact
// shape of failure the previous code was written to refuse, and which it refused by carrying
// on.
func TestAModuleThatAsksForADatabaseAndGetsNoneIsRefused(t *testing.T) {
	answer := registrationAnswer{Resources: map[string]resourceHandover{}}

	_, err := applyHandover(&answer)
	if err == nil {
		t.Fatal("a module with no database registered as though it had one")
	}
	if !strings.Contains(err.Error(), historySlot) {
		t.Errorf("the refusal does not name the slot that was not filled: %v", err)
	}
}

// A handover that arrived but lost part of itself on the way. Refused by name rather than
// assembled into a DSN with a hole in it, which fails at connect time as a refused socket.
func TestAHandoverMissingAPartIsRefused(t *testing.T) {
	given := resourceHandover{}
	given.Payload.Host = "postgres"
	given.Payload.Port = "5432"
	given.Payload.Database = "dogit_deploy"
	// No user, no password.

	answer := registrationAnswer{Resources: map[string]resourceHandover{historySlot: given}}
	_, err := applyHandover(&answer)
	if err == nil {
		t.Fatal("a handover with no user and no password was accepted")
	}
	named := err.Error()
	for _, want := range []string{"user", "password"} {
		if !strings.Contains(named, want) {
			t.Errorf("%q is not named as missing in %v", want, named)
		}
	}
}
