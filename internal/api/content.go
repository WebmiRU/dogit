package api

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/models"
)

// languageByExtension maps file extensions to the identifiers the frontend
// highlighter uses. Keeping the table on the server means the client does not
// have to ship a second copy, and unknown files simply get no highlighting.
var languageByExtension = map[string]string{
	".go":         "go",
	".mod":        "go",
	".sum":        "text",
	".js":         "javascript",
	".mjs":        "javascript",
	".cjs":        "javascript",
	".ts":         "typescript",
	".tsx":        "typescript",
	".jsx":        "javascript",
	".vue":        "vue",
	".json":       "json",
	".yaml":       "yaml",
	".yml":        "yaml",
	".toml":       "toml",
	".md":         "markdown",
	".markdown":   "markdown",
	".html":       "xml",
	".xml":        "xml",
	".svg":        "xml",
	".css":        "css",
	".scss":       "scss",
	".sass":       "sass",
	".less":       "less",
	".sh":         "bash",
	".bash":       "bash",
	".zsh":        "bash",
	".fish":       "bash",
	".sql":        "sql",
	".py":         "python",
	".rb":         "ruby",
	".rs":         "rust",
	".java":       "java",
	".kt":         "kotlin",
	".c":          "c",
	".h":          "c",
	".cpp":        "cpp",
	".cc":         "cpp",
	".hpp":        "cpp",
	".cs":         "csharp",
	".php":        "php",
	".swift":      "swift",
	".ini":        "ini",
	".cfg":        "ini",
	".conf":       "ini",
	".env":        "bash",
	".dockerfile": "dockerfile",
	".diff":       "diff",
	".patch":      "diff",
}

// detectLanguage returns a highlighter hint for a repository path.
func detectLanguage(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	if lang, ok := languageByExtension[ext]; ok {
		return lang
	}

	base := strings.ToLower(filepath.Base(filePath))
	switch base {
	case "dockerfile", "containerfile":
		return "dockerfile"
	case "makefile", "gnumakefile":
		return "makefile"
	case "readme", "license", "licence", "notice", "authors", "changelog":
		return "markdown"
	case ".gitignore", ".dockerignore", ".npmignore":
		return "text"
	}

	// Extension-less files are common in shell-heavy repositories.
	if filepath.Ext(base) == "" {
		return ""
	}
	return ""
}

// contentTypeFor returns the Content-Type used when a file is downloaded
// verbatim. The list is intentionally short: anything unknown is served as a
// download rather than guessed, which avoids a stored-XSS vector.
func contentTypeFor(filePath string) string {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".html", ".htm":
		return "text/plain; charset=utf-8"
	case ".svg":
		return "text/plain; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".ico":
		return "image/x-icon"
	case ".zip":
		return "application/zip"
	case ".gz", ".tgz":
		return "application/gzip"
	}
	return "text/plain; charset=utf-8"
}

// validateGitRefName wraps the git-level validator so handlers return a
// user-facing error rather than a boolean.
func validateGitRefName(name string) error {
	return gitx.ValidateRefName(name)
}

// seedReadme gives a brand new repository its first commit, so the web UI has
// something to show instead of an empty-state screen.
func (s *Server) seedReadme(r *http.Request, project *models.Project) error {
	repoDir := s.repos.PathFor(project)

	content := "# " + project.Name + "\n\n" +
		"This repository is empty apart from this file.\n\n" +
		"Clone it and push your first commit:\n\n" +
		"```\ngit clone " + s.cfg.CloneURL(project.Path) + "\n```\n"

	_, tree, err := s.git.WriteBlobAndTree(r.Context(), repoDir, "", "README.md", []byte(content))
	if err != nil {
		return err
	}

	commit, err := s.git.CommitTree(r.Context(), repoDir, gitx.CommitTreeOptions{
		Tree:           tree,
		Message:        "Initial commit",
		AuthorName:     userFrom(r.Context()).Name,
		AuthorEmail:    userFrom(r.Context()).Email,
		CommitterName:  userFrom(r.Context()).Name,
		CommitterEmail: userFrom(r.Context()).Email,
	})
	if err != nil {
		return err
	}

	branch := project.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	if err := s.git.CreateBranch(r.Context(), repoDir, branch, commit); err != nil {
		return err
	}
	return s.git.UpdateServerInfo(r.Context(), repoDir)
}
