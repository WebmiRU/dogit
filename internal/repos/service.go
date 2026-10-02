// Package repos owns the lifecycle of bare git repositories: creation on disk,
// hook installation and safe path resolution.
package repos

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/gitserver"
	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/hooks"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// Service creates and looks up repositories.
type Service struct {
	store    *store.Store
	git      *gitx.Git
	repoRoot string
}

func New(st *store.Store, git *gitx.Git, repoRoot string) *Service {
	return &Service{store: st, git: git, repoRoot: repoRoot}
}

// CreateParams describes a new project.
type CreateParams struct {
	// Path is the canonical path: "project" or "group/project".
	Path        string
	Name        string
	Description string
	Visibility  string
	OwnerID     uuid.UUID
	GroupID     *uuid.UUID
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,254}$`)

// maxPathLength bounds the whole path, not just a component: the value becomes
// part of a URL, a clone string and a directory path, and unbounded nesting
// would break all three.
const maxPathLength = 255

// ValidPath reports whether p is a usable project path. Group slugs and project
// paths share the same rule, which keeps SSH and HTTP lookups unambiguous.
func ValidPath(p string) bool {
	if p == "" || len(p) > maxPathLength {
		return false
	}
	if strings.HasSuffix(p, ".git") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if !slugRe.MatchString(part) {
			return false
		}
	}
	return true
}

// Create creates the project row and its bare repository.
//
// The database row is written first: a repository on disk with no row is
// invisible and harmless, whereas a row with no repository breaks every
// subsequent push.
func (s *Service) Create(ctx context.Context, p CreateParams) (*models.Project, error) {
	p.Path = strings.ToLower(strings.Trim(p.Path, "/"))
	if !ValidPath(p.Path) {
		return nil, fmt.Errorf("invalid project path %q", p.Path)
	}
	if p.Name == "" {
		p.Name = lastSegment(p.Path)
	}
	if p.Visibility == "" {
		p.Visibility = "private"
	}
	switch p.Visibility {
	case "private", "internal", "public":
	default:
		return nil, fmt.Errorf("invalid visibility %q", p.Visibility)
	}

	repoDir, err := gitserver.ResolveRepo(s.repoRoot, p.Path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(repoDir); err == nil {
		return nil, fmt.Errorf("repository %s already exists on disk", repoDir)
	}

	project := &models.Project{
		GroupID:       p.GroupID,
		Path:          p.Path,
		Name:          p.Name,
		Description:   p.Description,
		Visibility:    p.Visibility,
		DefaultBranch: "main",
		MergeMethod:   "merge",
		RepoPath:      repoDir,
	}
	if err := s.store.Projects().Create(ctx, project); err != nil {
		return nil, err
	}

	if err := s.git.InitBare(ctx, repoDir); err != nil {
		return nil, fmt.Errorf("initialise repository: %w", err)
	}
	if err := hooks.Install(ctx, s.git, repoDir, p.Path); err != nil {
		return nil, err
	}
	return project, nil
}

// Delete removes the repository from disk after deleting its row.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	project, err := s.store.Projects().ByID(ctx, id)
	if err != nil {
		return err
	}
	if err := s.store.Projects().Delete(ctx, id); err != nil {
		return err
	}
	if project.RepoPath == "" {
		return nil
	}
	if err := os.RemoveAll(project.RepoPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove repository: %w", err)
	}
	return nil
}

// PathFor returns the on-disk directory of a project.
func (s *Service) PathFor(project *models.Project) string {
	if project.RepoPath != "" {
		return project.RepoPath
	}
	return filepath.Join(s.repoRoot, gitserver.NormalizePath(project.Path)+".git")
}

func lastSegment(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

// MoveResult describes what a move changed.
type MoveResult struct {
	FromPath string
	ToPath   string
	FromDir  string
	ToDir    string
}

// Move puts a project at a new path, which is what moving it into or out of a
// group amounts to.
//
// A project's path is not only its name in the interface: the repository
// directory is derived from it, and the hook that reports pushes reads the path
// out of the repository's own configuration. All three move together, and in
// that order, with the rename undone if a later step fails — a project whose
// row says one path and whose directory says another is unreachable and hard to
// tell apart from a permission problem.
func (s *Service) Move(ctx context.Context, project *models.Project, newPath string, groupID *uuid.UUID) (*MoveResult, error) {
	fromPath := project.Path
	newPath = strings.ToLower(strings.Trim(newPath, "/"))
	if !ValidPath(newPath) {
		return nil, fmt.Errorf("invalid project path %q", newPath)
	}
	if newPath == project.Path {
		// Same address, different owner: nothing on disk moves.
		if err := s.store.Projects().SetGroup(ctx, project.ID, groupID); err != nil {
			return nil, err
		}
		project.Path, project.GroupID = newPath, groupID
		return &MoveResult{FromPath: fromPath, ToPath: newPath}, nil
	}

	fromDir, err := gitserver.ResolveRepo(s.repoRoot, project.Path)
	if err != nil {
		return nil, err
	}
	toDir, err := gitserver.ResolveRepo(s.repoRoot, newPath)
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(toDir); err == nil {
		return nil, fmt.Errorf("%s already exists", newPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	// The parent has to exist before the directory can be renamed into it.
	if parent := filepath.Dir(toDir); parent != filepath.Dir(fromDir) {
		if err := os.MkdirAll(parent, 0o750); err != nil {
			return nil, fmt.Errorf("create the group directory: %w", err)
		}
	}

	if err := os.Rename(fromDir, toDir); err != nil {
		return nil, fmt.Errorf("move the repository: %w", err)
	}

	rollback := func(cause error) (*MoveResult, error) {
		if err := os.Rename(toDir, fromDir); err != nil {
			// Both the rename and the undo failed, and the caller has to be told
			// where the repository now is rather than left guessing.
			return nil, fmt.Errorf("%w (the repository is now at %s and could not be moved back: %v)",
				cause, toDir, err)
		}
		return nil, cause
	}

	if err := s.git.SetConfig(ctx, toDir, hooks.ConfigProjectPath, newPath); err != nil {
		return rollback(fmt.Errorf("record the new path in the repository: %w", err))
	}

	if err := s.store.Projects().SetGroup(ctx, project.ID, groupID); err != nil {
		return rollback(fmt.Errorf("record the new path: %w", err))
	}
	if err := s.store.Projects().Rename(ctx, project.ID, newPath); err != nil {
		return rollback(fmt.Errorf("record the new name: %w", err))
	}

	project.Path, project.GroupID, project.RepoPath = newPath, groupID, toDir
	return &MoveResult{FromPath: project.Path, ToPath: newPath, FromDir: fromDir, ToDir: toDir}, nil
}
