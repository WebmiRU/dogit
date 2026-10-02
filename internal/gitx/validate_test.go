package gitx

import "testing"

func TestValidateRefName(t *testing.T) {
	valid := []string{
		"main", "feature/login", "release-1.2", "v1.0.0", "fix_123",
	}
	for _, name := range valid {
		if err := ValidateRefName(name); err != nil {
			t.Errorf("expected %q to be valid, got %v", name, err)
		}
	}

	// Names git itself rejects, or that would let a caller escape the ref
	// namespace, must fail before we build a command line.
	invalid := []string{
		"", "..", "../etc", "a..b", "-flag", "has space", "has~tilde", "has^caret",
		"has:colon", "has?question", "has*star", "has[bracket", "has\\backslash",
		"trailing/", "/leading", "trailing.", "ends.lock", "@", "has@{now}",
		"new\nline", ".hidden", "group/./x",
	}
	for _, name := range invalid {
		if err := ValidateRefName(name); err == nil {
			t.Errorf("expected %q to be rejected", name)
		}
	}
}

func TestValidateRefFull(t *testing.T) {
	if err := ValidateRefFull("refs/heads/main"); err != nil {
		t.Errorf("expected refs/heads/main to be valid, got %v", err)
	}
	if err := ValidateRefFull("main"); err == nil {
		t.Error("expected a bare ref name to be rejected")
	}
	if err := ValidateRefFull("refs/heads/../evil"); err == nil {
		t.Error("expected traversal to be rejected")
	}
}

func TestValidateRepoPath(t *testing.T) {
	for _, path := range []string{"README.md", "src/app/main.go", "a/b/c.txt"} {
		if err := ValidateRepoPath(path); err != nil {
			t.Errorf("expected %q to be valid, got %v", path, err)
		}
	}
	// ".git" and friends must never be reachable through the API.
	for _, path := range []string{"../secrets", "a/../../etc", ".git/config", "a/.git/refs", ""} {
		if err := ValidateRepoPath(path); err == nil {
			t.Errorf("expected %q to be rejected", path)
		}
	}
}

func TestIsHexSHA(t *testing.T) {
	if !IsHexSHA("1ad5c24dab0a0a287bfb78d577e99141bc910825") {
		t.Error("expected a full SHA to be recognised")
	}
	for _, s := range []string{"", "1ad5c24", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"} {
		if IsHexSHA(s) {
			t.Errorf("expected %q not to be a full SHA", s)
		}
	}
}

func TestShortSHA(t *testing.T) {
	if got := ShortSHA("1ad5c24dab0a0a287bfb78d577e99141bc910825"); got != "1ad5c24d" {
		t.Errorf("got %q, want %q", got, "1ad5c24d")
	}
	if got := ShortSHA("abc"); got != "abc" {
		t.Errorf("got %q, want %q", got, "abc")
	}
}
