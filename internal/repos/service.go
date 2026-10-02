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
