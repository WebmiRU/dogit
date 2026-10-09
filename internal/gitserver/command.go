// Package gitserver holds the transport-independent half of git-over-ssh: the
// parsing of git commands, authorisation checks against the database, and the
// execution of git-upload-pack / git-receive-pack.
//
// Nothing in this package depends on how the connection arrived. In production
// the connection comes from the system OpenSSH server through an
// AuthorizedKeysCommand that force-executes dogit's hook entry point; the
// in-process SSH listener built on gliderlabs/ssh (DOGIT_SSH_MODE=builtin) is
// kept as a fallback for environments where a system sshd cannot be run.
package gitserver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Repository verbs understood by the server.
const (
	VerbUploadPack  = "git-upload-pack"
	VerbReceivePack = "git-receive-pack"
)

// ParseGitCommand extracts the verb and repository path from an SSH exec
// command line. Accepted forms:
//
//	git-upload-pack '/group/project.git'
//	git-upload-pack '/group/project.git --stateless-rpc'
//	git-receive-pack "group/project.git"
//
// Trailing client flags are discarded: only the verb and the path matter.
func ParseGitCommand(raw string) (verb, repoPath string, err error) {
	cmd := strings.TrimSpace(raw)
	if cmd == "" {
		return "", "", errors.New("empty command")
	}

	fields := strings.Fields(cmd)
	verb = fields[0]

	switch verb {
	case VerbUploadPack, VerbReceivePack:
	default:
		return "", "", fmt.Errorf("command %q is not allowed on this server", fields[0])
	}

	if len(fields) < 2 {
		return "", "", errors.New("missing repository path")
	}

	repoPath = strings.Trim(fields[1], `"'`)
	if i := strings.IndexAny(repoPath, " \t"); i >= 0 {
		repoPath = repoPath[:i]
	}
	if repoPath == "" {
		return "", "", errors.New("missing repository path")
	}

	// Reject anything that is not a plain path before it reaches the filesystem.
	if strings.Contains(repoPath, "..") ||
		strings.ContainsAny(repoPath, "\\$`;&|<>()\n\r") {
		return "", "", fmt.Errorf("invalid repository path %q", repoPath)
	}
	return verb, repoPath, nil
}

// NormalizePath turns a requested repository path into the canonical form used
// both as the projects.path key and as the directory name inside the repo root.
func NormalizePath(requested string) string {
	p := strings.TrimSuffix(strings.Trim(strings.TrimSpace(requested), `"'`), ".git")
	return strings.Trim(p, "/")
}

// ResolveRepo maps a requested repository path to an absolute directory inside
// repoRoot, refusing anything that would escape it.
func ResolveRepo(repoRoot, requested string) (string, error) {
	cleaned := NormalizePath(requested)
	if cleaned == "" {
		return "", errors.New("empty repository path")
	}
	if strings.ContainsAny(cleaned, `\\`) || strings.Contains(cleaned, "..") {
		return "", fmt.Errorf("invalid repository path %q", requested)
	}
	for _, part := range strings.Split(cleaned, "/") {
		// Reject empty components and dotfiles (.git, .ssh, hidden dirs).
		if part == "" || strings.HasPrefix(part, ".") {
			return "", fmt.Errorf("invalid repository path %q", requested)
		}
	}

	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", err
	}
	full, err := filepath.Abs(filepath.Join(repoRoot, cleaned+".git"))
	if err != nil {
		return "", err
	}
	if full != root && !strings.HasPrefix(full, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid repository path %q", requested)
	}
	return full, nil
}

// RepoPathFor returns the directory a project is stored in.
func RepoPathFor(repoRoot, projectPath string) string {
	return filepath.Join(repoRoot, NormalizePath(projectPath)+".git")
}
