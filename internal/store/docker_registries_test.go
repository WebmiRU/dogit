package store_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/store"
)

// One address, however it is written down.
//
// Everything that compares addresses strips the scheme before it compares, and everything
// that carries one around strips it too. A lookup that wanted the scheme back matched
// neither: "https://registry.example.com" on the row and "registry.example.com" in the
// caller's hand are the same address, and a registry nobody can look up is a registry
// that can only be reached by guessing its exact spelling. A deployment then refused with
// "not in the list of registries" for a registry plainly on the list.
func TestARegistryIsFoundByItsAddressHoweverItIsWritten(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	written := &store.DockerRegistry{
		Name: "harbor", URL: "https://registry.example.com",
		Login: "robot", Password: "s3cret", Enabled: true,
	}
	if err := st.DockerRegistries().Create(ctx, written); err != nil {
		t.Fatalf("write the registry down: %v", err)
	}
	t.Cleanup(func() { _ = st.DockerRegistries().Delete(ctx, written.ID) })

	for _, asked := range []string{
		"https://registry.example.com",  // as written down
		"registry.example.com",          // as every comparison in this project carries it
		"HTTPS://Registry.Example.com",  // and neither case nor scheme is the thing
		"https://registry.example.com/", // or a trailing slash, which is the same address
	} {
		found, err := st.DockerRegistries().ByURL(ctx, asked)
		if err != nil {
			t.Errorf("asked for %q: %v", asked, err)
			continue
		}
		if found.ID != written.ID {
			t.Errorf("asked for %q and got %q", asked, found.URL)
		}
	}

	// And a different address is still a different address: loosening the comparison
	// must not make everything match.
	if _, err := st.DockerRegistries().ByURL(ctx, "registry.example.org"); err == nil {
		t.Error("a registry that was never written down was found")
	}
}

// The list is an address book, so the first thing it has to get right is that one address
// is one row. Two records for one registry are one registry and two opinions about it,
// and every later reader would have to decide which one it meant.
func TestTwoRecordsForOneAddressAreAConflict(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	first := store.DockerRegistry{URL: dbtest.Unique("registry.example.com") + ":5000"}
	if err := st.DockerRegistries().Create(ctx, &first); err != nil {
		t.Fatalf("write the registry down: %v", err)
	}

	second := store.DockerRegistry{URL: first.URL}
	if err := st.DockerRegistries().Create(ctx, &second); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a second record for one address: %v, want a conflict", err)
	}
}

// And the same address written in another case is still the same address: a hostname does
// not care how it was typed, and a list that holds both is a list with a duplicate on it
// that the constraint cannot see.
func TestTheSameAddressInAnotherCaseIsTheSameAddress(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	host := dbtest.Unique("registry.example.com")
	written := store.DockerRegistry{URL: host + ":5000"}
	if err := st.DockerRegistries().Create(ctx, &written); err != nil {
		t.Fatalf("write the registry down: %v", err)
	}

	shouted := store.DockerRegistry{URL: host + ":5000", Name: "the same one again"}
	if err := st.DockerRegistries().Create(ctx, &shouted); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("the same address in another case: %v, want a conflict", err)
	}

	found, err := st.DockerRegistries().ByURL(ctx, host+":5000")
	if err != nil {
		t.Fatalf("find it by address: %v", err)
	}
	if found.ID != written.ID {
		t.Fatalf("asking by address found %s, not the record that was written", found.ID)
	}
}

// A registry is reached by an address somebody typed, and the trailing slash is the same
// address with one more character on the end. Stripped on the way in, so that what the
// table holds is what a reader would call the same place.
func TestATrailingSlashIsNotADifferentAddress(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	host := dbtest.Unique("registry.example.com")
	reg := store.DockerRegistry{URL: host + ":5000/"}
	if err := st.DockerRegistries().Create(ctx, &reg); err != nil {
		t.Fatalf("write the registry down: %v", err)
	}
	if reg.URL != host+":5000" {
		t.Fatalf("stored as %q, want the address without the slash", reg.URL)
	}
}

// There is no such thing as the default one, and a list where every row is an ordinary
// row has one less thing to be wrong about: writing to a record changes that record, and
// nothing else anywhere in the table.
func TestWritingToARegistryChangesNothingAboutTheOthers(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	first := store.DockerRegistry{URL: dbtest.Unique("first.example.com"), Note: "the first one"}
	if err := st.DockerRegistries().Create(ctx, &first); err != nil {
		t.Fatalf("write the first registry down: %v", err)
	}
	second := store.DockerRegistry{URL: dbtest.Unique("second.example.com"), Note: "the second one"}
	if err := st.DockerRegistries().Create(ctx, &second); err != nil {
		t.Fatalf("write the second registry down: %v", err)
	}

	first.Note = "edited"
	if err := st.DockerRegistries().Update(ctx, &first); err != nil {
		t.Fatalf("edit the first one: %v", err)
	}

	// The neighbour is a record of its own, and the way a page learned it is an ordinary
	// page of a list — so an edit of one row cannot reach it.
	after, err := st.DockerRegistries().ByID(ctx, second.ID)
	if err != nil {
		t.Fatalf("read the second registry back: %v", err)
	}
	if after.Note != "the second one" {
		t.Errorf("editing one registry changed its neighbour's note to %q", after.Note)
	}
	if !after.UpdatedAt.Equal(second.UpdatedAt) {
		t.Errorf("editing one registry touched its neighbour's timestamp: %v, want %v",
			after.UpdatedAt, second.UpdatedAt)
	}
}

// Everything a registry is, survives a round trip — including the credential, which is
// stored and not returned, and the three decisions that are not derivable from the
// address.
func TestARegistryKeepsWhatItWasGiven(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	written := store.DockerRegistry{
		Name:        "internal mirror",
		URL:         dbtest.Unique("192.168.1.103") + ":8091",
		Login:       "robot$deployer",
		Password:    "hunter2",
		InsecureTLS: true,
		ReadOnly:    true,
		Note:        "written down because the address was in shell history",
		Enabled:     true,
	}
	if err := st.DockerRegistries().Create(ctx, &written); err != nil {
		t.Fatalf("write the registry down: %v", err)
	}

	read, err := st.DockerRegistries().ByID(ctx, written.ID)
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if read.Name != written.Name || read.URL != written.URL || read.Login != written.Login ||
		read.Password != written.Password || read.InsecureTLS != written.InsecureTLS ||
		read.ReadOnly != written.ReadOnly || read.Note != written.Note || read.Enabled != written.Enabled {
		t.Errorf("came back as %+v, not what was written (%+v)", read, written)
	}
	if read.CreatedAt.IsZero() || read.UpdatedAt.IsZero() {
		t.Error("the record carries no timestamps")
	}
}

// Deleting removes the record and only the record, so that a forgotten registry is not a
// reason anything outside this table can be reached.
func TestDeletingRemovesOnlyTheRecord(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	reg := store.DockerRegistry{URL: dbtest.Unique("gone.example.com")}
	if err := st.DockerRegistries().Create(ctx, &reg); err != nil {
		t.Fatalf("write the registry down: %v", err)
	}
	if err := st.DockerRegistries().Delete(ctx, reg.ID); err != nil {
		t.Fatalf("take it off the list: %v", err)
	}
	if _, err := st.DockerRegistries().ByID(ctx, reg.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reading it after deleting: %v, want not found", err)
	}
	if err := st.DockerRegistries().Delete(ctx, reg.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleting it twice: %v, want not found", err)
	}
}

// The list is paginated and says how much of it there is, because a page that says
// "page 1" and stops is a page nobody can find the end of.
func TestTheListIsAPageAndKnowsTheRest(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	// The table is shared with every other test in this package, so the total is read
	// before and after rather than assumed: what this test checks is that 23 records
	// written make 23 more records counted, and that ten of them are a page.
	_, before, err := st.DockerRegistries().List(ctx, store.DockerRegistryListFilter{Limit: 1})
	if err != nil {
		t.Fatalf("count what is already on the list: %v", err)
	}

	const written = 23
	for i := 0; i < written; i++ {
		reg := store.DockerRegistry{URL: dbtest.Unique("many.example.com") + ":" + strconv.Itoa(i)}
		if err := st.DockerRegistries().Create(ctx, &reg); err != nil {
			t.Fatalf("write a registry down: %v", err)
		}
	}

	// The last page of what this test wrote starts after everything that was already
	// there, for the same reason the count is read before: the list is one list.
	page, total, err := st.DockerRegistries().List(ctx,
		store.DockerRegistryListFilter{Limit: 10, Offset: before + 20})
	if err != nil {
		t.Fatalf("read the last page: %v", err)
	}
	if want := before + written; total != want {
		t.Fatalf("counted %d records, %d were on the list and %d were written", total, before, written)
	}
	if len(page) != 3 {
		t.Fatalf("the last page held %d records, want the 3 that are left over", len(page))
	}

	first, total, err := st.DockerRegistries().List(ctx, store.DockerRegistryListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("read the first page: %v", err)
	}
	if len(first) != 10 {
		t.Fatalf("the first page held %d records, want 10", len(first))
	}
	if want := before + written; total != want {
		t.Fatalf("counted %d records on the first page, %d were written", total, written)
	}
}

// A page past the end of the list has no records in it and must still say how many there
// are. The count arrives on the first row of a page, so an empty page answers nothing at
// all unless it is asked again — which is what makes "page 9 of a two-page list" read as
// an empty registry list rather than as a list a reader has gone past the end of.
func TestAPagePastTheEndStillKnowsHowManyThereAre(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	_, before, err := st.DockerRegistries().List(ctx, store.DockerRegistryListFilter{Limit: 1})
	if err != nil {
		t.Fatalf("count what is already on the list: %v", err)
	}
	for i := 0; i < 3; i++ {
		reg := store.DockerRegistry{URL: dbtest.Unique("short.example.com") + ":" + strconv.Itoa(i)}
		if err := st.DockerRegistries().Create(ctx, &reg); err != nil {
			t.Fatalf("write a registry down: %v", err)
		}
	}

	page, total, err := st.DockerRegistries().List(ctx,
		store.DockerRegistryListFilter{Limit: 10, Offset: 1000})
	if err != nil {
		t.Fatalf("read a page past the end: %v", err)
	}
	if len(page) != 0 {
		t.Fatalf("a page past the end held %d records", len(page))
	}
	if want := before + 3; total != want {
		t.Fatalf("counted %d records on an empty page, %d were on the list", total, want)
	}
}

// The one thing the table refuses is a second record for an address that is already on the
// list, and the refusal names that address — it is the only thing anybody can do about it,
// and it is why the uniqueness is case-insensitive: an address that differs from a written
// one only in case names the same machine, and two records for one machine would mean a
// deployment could pull from whichever it read first.
func TestTheRefusalNamesTheAddressThatIsAlreadyThere(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	written := store.DockerRegistry{URL: dbtest.Unique("held.example.com")}
	if err := st.DockerRegistries().Create(ctx, &written); err != nil {
		t.Fatalf("write a registry down: %v", err)
	}

	// The same machine written in another case, which is a duplicate and not a second
	// registry: two records for one host is a list where a deployment pulls from whichever
	// row it read first.
	same := store.DockerRegistry{URL: strings.ToUpper(written.URL)}
	err := st.DockerRegistries().Create(ctx, &same)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a second record for one address: %v, want a conflict", err)
	}
	// The refusal names the address as it was written in the record being refused, which is
	// what the person who wrote it can search for — hence the case-insensitive comparison
	// against the address already on the list.
	if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(written.URL)) {
		t.Errorf("the refusal does not name the address that is already on the list: %v", err)
	}
}

// there says so rather than answering with an empty one.
func TestReadingOneThatIsNotThere(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	if _, err := st.DockerRegistries().ByID(ctx, uuid.New()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reading an id that was never written: %v, want not found", err)
	}
	if _, err := st.DockerRegistries().ByURL(ctx, dbtest.Unique("never.example.com")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reading an address that was never written: %v, want not found", err)
	}
}
