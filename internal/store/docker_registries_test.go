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

// The default is one record's flag, not a paragraph of convention. Marking one default
// has to take the flag off whichever record had it, in the same breath — and the table's
// index is what makes "at the same time" the only arrangement available.
func TestThereIsOneDefaultAndMarkingAnotherMovesIt(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	first := store.DockerRegistry{URL: dbtest.Unique("first.example.com")}
	if err := st.DockerRegistries().Create(ctx, &first); err != nil {
		t.Fatalf("write the first registry down: %v", err)
	}
	second := store.DockerRegistry{URL: dbtest.Unique("second.example.com")}
	if err := st.DockerRegistries().Create(ctx, &second); err != nil {
		t.Fatalf("write the second registry down: %v", err)
	}

	// The way an administrator does it: mark one, and whoever had it stops having it
	// without anybody having to remember to.
	first.Default = true
	if err := st.DockerRegistries().Update(ctx, &first); err != nil {
		t.Fatalf("mark the first one the default: %v", err)
	}

	// A second record cannot be created as the default while the first holds it: the table
	// permits one, and it says so rather than letting both be true at once.
	third := store.DockerRegistry{URL: dbtest.Unique("third.example.com"), Default: true}
	if err := st.DockerRegistries().Create(ctx, &third); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a second default: %v, want a conflict", err)
	}

	// And marking the other one moves it.
	second.Default = true
	if err := st.DockerRegistries().Update(ctx, &second); err != nil {
		t.Fatalf("move the default: %v", err)
	}

	wasDefault, err := st.DockerRegistries().ByID(ctx, first.ID)
	if err != nil {
		t.Fatalf("read the first registry back: %v", err)
	}
	if wasDefault.Default {
		t.Error("the first registry is still marked default after another was marked")
	}
	isDefault, err := st.DockerRegistries().ByID(ctx, second.ID)
	if err != nil {
		t.Fatalf("read the second registry back: %v", err)
	}
	if !isDefault.Default {
		t.Error("the second registry was marked default and is not")
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

// The table refuses two different things, and it must refuse each with the truth about it.
// Told "that address is already on the list" for a record whose address is not on the list,
// an administrator goes looking for a duplicate they did not write, and the registry they
// were trying to add stays unwritten.
func TestARefusalSaysWhichThingWasAlreadyThere(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	held := store.DockerRegistry{URL: dbtest.Unique("held.example.com")}
	if err := st.DockerRegistries().Create(ctx, &held); err != nil {
		t.Fatalf("write a registry down: %v", err)
	}
	held.Default = true
	if err := st.DockerRegistries().Update(ctx, &held); err != nil {
		t.Fatalf("mark it the default: %v", err)
	}

	// A different address, refused because the default is taken rather than because the
	// address is on the list — and the sentence must not claim the address is.
	other := store.DockerRegistry{URL: dbtest.Unique("other.example.com"), Default: true}
	err := st.DockerRegistries().Create(ctx, &other)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a second default: %v, want a conflict", err)
	}
	if strings.Contains(err.Error(), other.URL) {
		t.Errorf("the refusal names %q, which is not on the list: %v", other.URL, err)
	}
	if !strings.Contains(err.Error(), "default") {
		t.Errorf("the refusal does not say what is in the way: %v", err)
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
