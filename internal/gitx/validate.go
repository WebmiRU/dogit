package gitx

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrNotFound is returned when an object, ref or path does not exist.
var ErrNotFound = errors.New("not found in repository")

// ErrInvalidRef is returned for ref names git would reject.
var ErrInvalidRef = errors.New("invalid reference name")

// git-check-ref-format rules, expressed directly so that invalid names are
// rejected before we ever build a command line.
var (
	refNameRe       = regexp.MustCompile(`^[A-Za-z0-9._\-/]+$`)
	invalidRefChars = regexp.MustCompile(`[\x00-\x20\x7f~^:?*\[\\\x{2000}-\x{206f}]`)
	refComponentRe  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	dotDotRe        = regexp.MustCompile(`\.\.`)
)

// ValidateRefName checks a branch or tag name against git's rules, plus the
// extra restriction that names must not start with or end with a separator.
func ValidateRefName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: empty name", ErrInvalidRef)
	case len(name) > 255:
		return fmt.Errorf("%w: name too long", ErrInvalidRef)
	case invalidRefChars.MatchString(name):
		return fmt.Errorf("%w: %q contains forbidden characters", ErrInvalidRef, name)
	case dotDotRe.MatchString(name):
		return fmt.Errorf("%w: %q contains '..'", ErrInvalidRef, name)
	case strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/"):
		return fmt.Errorf("%w: %q must not start or end with '/'", ErrInvalidRef, name)
	case strings.HasPrefix(name, "-"):
		return fmt.Errorf("%w: %q must not start with '-'", ErrInvalidRef, name)
	case strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock"):
		return fmt.Errorf("%w: %q has an invalid suffix", ErrInvalidRef, name)
	case strings.Contains(name, "@{"):
		return fmt.Errorf("%w: %q contains '@{'", ErrInvalidRef, name)
	case name == "@":
		return fmt.Errorf("%w: '@' is reserved", ErrInvalidRef)
	}
	for _, part := range strings.Split(name, "/") {
		if !refComponentRe.MatchString(part) {
			return fmt.Errorf("%w: %q has an invalid path component", ErrInvalidRef, name)
		}
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return fmt.Errorf("%w: %q has an invalid path component", ErrInvalidRef, name)
		}
	}
	_ = refNameRe
	return nil
}

// ValidateRefFull validates a fully qualified ref such as refs/heads/main.
func ValidateRefFull(ref string) error {
	if !strings.HasPrefix(ref, "refs/") {
		return fmt.Errorf("%w: %q must start with refs/", ErrInvalidRef, ref)
	}
	return ValidateRefName(strings.TrimPrefix(ref, "refs/"))
}

// PathEntryRejectsTraversal guards repository paths coming from HTTP requests.
var PathEntryRejectsTraversal = func(path string) bool {
	for _, part := range strings.Split(path, "/") {
		if part == ".." || part == "" || part == "." {
			return true
		}
		// ".git" itself is the repository's own metadata and nothing else may
		// claim that name. Files that merely begin with those letters are ordinary
		// files — ".gitlab-ci.yml" is the name every CI configuration has, and
		// refusing it would make it impossible to add one at all.
		if part == ".git" {
			return true
		}
	}
	return false
}

// ValidateRepoPath checks a path relative to the repository root.
func ValidateRepoPath(path string) error {
	if PathEntryRejectsTraversal(path) {
		return fmt.Errorf("invalid repository path %q", path)
	}
	return nil
}
