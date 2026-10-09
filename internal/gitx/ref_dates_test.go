package gitx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// What a branch and a tag say about themselves beyond their names.
//
// The dates matter more than they look: they are read out of a format string and parsed, and a
// date that does not parse does not fail — it becomes the zero time, which a page prints as a
// year nobody has heard of. That is not a bug anybody reports, because every date is present and
// every date is wrong in the same way.

// datedTestRepo is a bare repository plus a way to say more about it.
//
// The shared fixture in repo_test.go is enough to list refs and cannot add a tag with a message or
// a tagger to it, and both of those are what these tests are about — so this one has its own and
// hands back the working directory's runner.
func datedTestRepo(t *testing.T) (git *Git, bare string, work string) {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	dir := t.TempDir()
	work = filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", work}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Tester", "GIT_AUTHOR_EMAIL=tester@example.com",
			"GIT_COMMITTER_NAME=Tester", "GIT_COMMITTER_EMAIL=tester@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	run("init", "-q", "-b", "main")
	name := filepath.Join(work, "file.txt")
	if err := os.WriteFile(name, []byte("line one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "the first change")

	bare = filepath.Join(dir, "repo.git")
	if out, err := exec.Command("git", "clone", "--bare", "-q", work, bare).CombinedOutput(); err != nil {
		t.Fatalf("clone --bare: %v\n%s", err, out)
	}

	// Everything the tests add is added to the bare repository rather than to the working copy: a
	// bare clone is independent of the repository it came from, so a tag made afterwards over
	// there is a tag the bare one has never heard of.
	return New(Options{}), bare, bare
}

// commitIn runs git in the bare repository, with an identity, so a tag can be made there.
func commitIn(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Tester", "GIT_AUTHOR_EMAIL=tester@example.com",
		"GIT_COMMITTER_NAME=Tester", "GIT_COMMITTER_EMAIL=tester@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// A tag carries who made it and what they said.
func TestATagSaysWhoMadeItAndWhatTheySaid(t *testing.T) {
	git, bare, work := datedTestRepo(t)
	commitIn(t, work, "tag", "-a", "v1.0", "-m", "first release")

	tags, err := git.Tags(context.Background(), bare)
	if err != nil {
		t.Fatalf("list the tags: %v", err)
	}
	if len(tags) != 1 {
		t.Fatalf("read %d tags, want 1: %+v", len(tags), tags)
	}

	tag := tags[0]
	if tag.Message != "first release" {
		t.Errorf("the tag says %q, want the message it was given", tag.Message)
	}
	if tag.CreatedBy == "" {
		t.Error("the tag names nobody, so an annotated tag is indistinguishable from a bare one")
	}
	if tag.CreatedAt.IsZero() {
		t.Error("the tag has no date, so the page will show the zero time")
	}
}

// The whole point of the date parsing: a real date, not a plausible number.
func TestADateIsADateAndNotTheYearNobodyHasHeardOf(t *testing.T) {
	git, bare, _ := datedTestRepo(t)

	branches, err := git.Branches(context.Background(), bare)
	if err != nil {
		t.Fatalf("list the branches: %v", err)
	}
	if len(branches) == 0 {
		t.Fatal("the repository has no branches to read a date from")
	}
	for _, branch := range branches {
		if branch.CreatedAt.Year() < 2000 {
			t.Errorf("branch %q has the date %s, which is before this repository existed",
				branch.Name, branch.CreatedAt)
		}
	}
}

// A lightweight tag is a name pointing at a commit: nobody made it and nothing was said, and the
// fields say so by being empty rather than by being wrong.
func TestABareTagSaysNothingRatherThanSomethingWrong(t *testing.T) {
	git, bare, work := datedTestRepo(t)
	commitIn(t, work, "tag", "lightweight")

	tags, err := git.Tags(context.Background(), bare)
	if err != nil {
		t.Fatalf("list the tags: %v", err)
	}
	if len(tags) != 1 {
		t.Fatalf("read %d tags, want 1: %+v", len(tags), tags)
	}
	if tags[0].Message != "" {
		t.Errorf("a bare tag claims to say %q", tags[0].Message)
	}
	if tags[0].CreatedBy != "" {
		t.Errorf("a bare tag claims to be made by %q", tags[0].CreatedBy)
	}
	// The date is still there: a lightweight tag points at a commit, and the commit has a date.
	if tags[0].CreatedAt.IsZero() {
		t.Error("a bare tag has no date at all, though the commit it points at does")
	}
}

// A date this package does not understand is no date. Guessing is what turns a format change into
// a page full of dates in the year 1.
func TestADateThatWillNotParseIsNoDate(t *testing.T) {
	for name, raw := range map[string]string{
		"git's loose form": "2026-10-08 20:23:47 +0300",
		"an empty field":   "",
		"nonsense":         "whenever",
		"a unix timestamp": "1760000000",
	} {
		t.Run(name, func(t *testing.T) {
			if got := parseRefDate(raw); !got.IsZero() {
				t.Errorf("%q parsed as %s, want no date at all", raw, got)
			}
		})
	}

	got := parseRefDate("2026-10-08T20:23:47+03:00")
	if got.IsZero() {
		t.Error("a strict ISO 8601 date did not parse, which is the only shape this asks git for")
	} else if got.Year() != 2026 {
		t.Errorf("the date parsed as %s", got)
	}
}

// A date in the future is what a misread format gives, and it is worth catching: nobody creates a
// tag tomorrow.
func TestNoDateIsInTheFuture(t *testing.T) {
	git, bare, work := datedTestRepo(t)
	commitIn(t, work, "tag", "-a", "v1.0", "-m", "first release")

	tomorrow := time.Now().Add(24 * time.Hour)
	tags, err := git.Tags(context.Background(), bare)
	if err != nil {
		t.Fatalf("list the tags: %v", err)
	}
	for _, tag := range tags {
		if !tag.CreatedAt.IsZero() && tag.CreatedAt.After(tomorrow) {
			t.Errorf("tag %q is dated %s, which is tomorrow", tag.Name, tag.CreatedAt)
		}
	}
}

// A tag with a message in it that contains the separator would be read as more fields than it has.
// Nobody writes that, and if they do the row is dropped rather than shown wrong — which is the
// outcome this checks rather than that the message survives.
func TestAMessageCannotSmuggleInAnotherField(t *testing.T) {
	git, bare, work := datedTestRepo(t)
	commitIn(t, work, "tag", "-a", "v1.0", "-m", fmt.Sprintf("one|two|three"))

	tags, err := git.Tags(context.Background(), bare)
	if err != nil {
		t.Fatalf("list the tags: %v", err)
	}
	for _, tag := range tags {
		if strings.Count(tag.Name, "|") > 0 {
			t.Errorf("the tag is named %q, which means the message was read as fields", tag.Name)
		}
	}
}
