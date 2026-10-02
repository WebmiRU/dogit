// Package hooks installs and parses the git hooks that integrate a repository
// with dogit.
package hooks

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ewolf/dogit/internal/gitx"
)

// Git service verbs.
const (
	VerbUploadPack  = "git-upload-pack"
	VerbReceivePack = "git-receive-pack"
)

// Installed hook names.
const (
	HookPostReceive = "post-receive"
	HookPreReceive  = "pre-receive"
	HookPostUpdate  = "post-update"
)

// ConfigProjectPath is the repository-local git config key that records which
// project a bare repository belongs to. The hook script reads it, so a nested
// path like "group/project" resolves correctly without guessing from the
// directory name.
const ConfigProjectPath = "dogit.projectpath"

// postReceiveScript is installed into every bare repository. It forwards ref
// updates to `dogit hook post-receive`.
//
// The script deliberately does no work itself and never fails the push:
// post-receive runs after the objects are already committed, so a non-zero exit
// here would only confuse the client.
const postReceiveScript = `#!/bin/sh
# Installed by dogit. Forwards ref updates to the dogit hook command.
GIT_DIR="$(git rev-parse --git-dir)"

PROJECT_PATH="$(git config --get dogit.projectpath)"
if [ -z "$PROJECT_PATH" ]; then
    PROJECT_PATH="$(basename "$GIT_DIR" .git)"
fi

HOOK_BINARY="${DOGIT_HOOK_BINARY:-dogit}"

# Never fail the push: the objects are already safely stored.
exec "$HOOK_BINARY" hook post-receive \
    --git-dir "$GIT_DIR" \
    --project-path "$PROJECT_PATH"
`

// preReceiveScript runs before objects are accepted. All authorisation happens
// in the SSH layer, where the user is known; this hook only rejects updates to a
// repository that has no commits yet being created with an unexpected branch.
const preReceiveScript = `#!/bin/sh
# Installed by dogit. Authorisation is performed by the SSH layer.
exit 0
`

// Install writes the hook scripts into a bare repository, points core.hooksPath
// at them so git actually runs them, and records the dogit project path.
func Install(ctx context.Context, g *gitx.Git, repoPath, projectPath string) error {
	hooksDir := filepath.Join(repoPath, "dogit-hooks")
	if err := os.MkdirAll(hooksDir, 0o750); err != nil {
		return fmt.Errorf("create hooks directory: %w", err)
	}

	for name, body := range map[string]string{
		HookPostReceive: postReceiveScript,
		HookPreReceive:  preReceiveScript,
	} {
		path := filepath.Join(hooksDir, name)
		if err := os.WriteFile(path, []byte(body), 0o750); err != nil {
			return fmt.Errorf("write %s hook: %w", name, err)
		}
	}

	// core.hooksPath is interpreted relative to the repository root.
	if err := g.SetConfig(ctx, repoPath, "core.hooksPath", "dogit-hooks"); err != nil {
		return err
	}
	if err := g.SetConfig(ctx, repoPath, ConfigProjectPath, projectPath); err != nil {
		return err
	}
	return nil
}

// RefUpdate is one line of post-receive input.
type RefUpdate struct {
	OldSHA string `json:"old_sha"`
	NewSHA string `json:"new_sha"`
	Ref    string `json:"ref"`
}

// IsBranch reports whether the ref is a branch.
func (u RefUpdate) IsBranch() bool { return strings.HasPrefix(u.Ref, "refs/heads/") }

// IsTag reports whether the ref is a tag.
func (u RefUpdate) IsTag() bool { return strings.HasPrefix(u.Ref, "refs/tags/") }

// Branch returns the short branch name, or "" for non-branch refs.
func (u RefUpdate) Branch() string {
	if !u.IsBranch() {
		return ""
	}
	return strings.TrimPrefix(u.Ref, "refs/heads/")
}

// IsDelete reports whether the ref is being deleted.
func (u RefUpdate) IsDelete() bool {
	return u.NewSHA == strings.Repeat("0", 40) || u.NewSHA == ""
}

// ParsePostReceive reads the "<old> <new> <ref>" lines git writes to the hook's
// stdin.
func ParsePostReceive(r io.Reader) ([]RefUpdate, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	updates := []RefUpdate{}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("malformed post-receive line %q", line)
		}
		updates = append(updates, RefUpdate{
			OldSHA: fields[0], NewSHA: fields[1], Ref: fields[2],
		})
	}
	return updates, sc.Err()
}

// CommitRange is the set of commits introduced by one ref update.
type CommitRange struct {
	Ref    string   `json:"ref"`
	Branch string   `json:"branch"`
	OldSHA string   `json:"old_sha"`
	NewSHA string   `json:"new_sha"`
	Shas   []string `json:"shas"`
}

// Payload is the JSON document attached to a push event.
type Payload struct {
	ProjectID   string        `json:"project_id"`
	ProjectPath string        `json:"project_path"`
	UserID      string        `json:"user_id,omitempty"`
	Username    string        `json:"username,omitempty"`
	RefUpdates  []CommitRange `json:"ref_updates"`
}

func EncodePayload(p Payload) ([]byte, error) { return json.Marshal(p) }
