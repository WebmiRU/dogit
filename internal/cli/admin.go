package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/app"
	"github.com/ewolf/dogit/internal/auth"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/repos"
	"github.com/ewolf/dogit/internal/store"
)

// User manages accounts from the command line. It exists for the first user
// (who must exist before the web UI can be used) and for recovery.
func User(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dogit user <create|list>")
	}
	switch args[0] {
	case "create", "add":
		return createUser(ctx, args[1:])
	case "list", "ls":
		return listUsers(ctx)
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func createUser(ctx context.Context, args []string) error {
	fs := newFlagSet("user create")
	username := fs.String("username", "", "login name (lowercase, no spaces)")
	email := fs.String("email", "", "email address")
	name := fs.String("name", "", "display name")
	password := fs.String("password", "", "password; omit to be prompted")
	isAdmin := fs.Bool("admin", false, "grant instance administrator rights")

	if err := parse(fs, args); err != nil {
		return err
	}

	if *username == "" || *email == "" {
		return fmt.Errorf("--username and --email are required")
	}
	*username = strings.ToLower(strings.TrimSpace(*username))

	pw := *password
	if pw == "" {
		var err error
		pw, err = readPassword(fmt.Sprintf("password for %s: ", *username))
		if err != nil {
			return err
		}
	}
	if err := auth.ValidatePassword(pw); err != nil {
		return err
	}

	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}

	a, err := app.New(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	user := &models.User{
		Username:     *username,
		Email:        *email,
		Name:         *name,
		PasswordHash: hash,
		IsAdmin:      *isAdmin,
	}
	if err := a.Store.Users().Create(ctx, user); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "created user %s (%s)%s\n",
		user.Username, user.Email, adminSuffix(user.IsAdmin))
	return nil
}

func listUsers(ctx context.Context) error {
	a, err := app.New(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	users, _, err := a.Store.Users().List(ctx, store.ListUsersFilter{Limit: 100})
	if err != nil {
		return err
	}
	for _, u := range users {
		fmt.Fprintf(os.Stdout, "%s\t%s\t%s\n", u.Username, u.Email, adminSuffix(u.IsAdmin))
	}
	return nil
}

func adminSuffix(admin bool) string {
	if admin {
		return " [admin]"
	}
	return ""
}

// Project creates projects from the command line, used for bootstrapping and
// for provisioning.
func Project(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "create" {
		return fmt.Errorf("usage: dogit project create <path>")
	}

	fs := newFlagSet("project create")
	path := fs.String("path", "", "project path, e.g. group/project")
	name := fs.String("name", "", "display name")
	description := fs.String("description", "", "short description")
	visibility := fs.String("visibility", "private", "private, internal or public")
	owner := fs.String("owner", "", "username of the creating user (required)")
	group := fs.String("group", "", "group path to nest the project under")
	_ = parse(fs, args[1:])

	if *path == "" || *owner == "" {
		return fmt.Errorf("--path and --owner are required")
	}

	a, err := app.New(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	user, err := a.Store.Users().ByUsername(ctx, *owner)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("user %q does not exist", *owner)
	}
	if err != nil {
		return err
	}

	var groupID *uuid.UUID
	if *group != "" {
		g, err := a.Store.Groups().ByPath(ctx, strings.Trim(*group, "/"))
		if err != nil {
			return fmt.Errorf("group %q not found", *group)
		}
		groupID = &g.ID
	}

	fullPath := strings.Trim(*path, "/")
	if groupID != nil {
		fullPath = strings.Trim(*group, "/") + "/" + fullPath
	}

	svc := repos.New(a.Store, a.Git, a.Cfg.RepoDir)
	project, err := svc.Create(ctx, repos.CreateParams{
		Path:        fullPath,
		Name:        *name,
		Description: *description,
		Visibility:  *visibility,
		OwnerID:     user.ID,
		GroupID:     groupID,
	})
	if err != nil {
		return err
	}

	// The creator becomes owner; this is what grants them manage_project.
	if _, err := a.Store.Permissions().AssignProjectRole(ctx, project.ID, "Owner",
		models.AccessLevelOwner, models.AccessLevelOwner, &user.ID, nil); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "created project %s\n", project.Path)
	// The canonical clone URL: no port and no ssh:// prefix, because the system
	// sshd terminates the connection.
	fmt.Fprintf(os.Stdout, "  clone: git@%s:%s.git\n", a.Cfg.SSHHost, project.Path)
	fmt.Fprintf(os.Stdout, "  path:  %s\n", svc.PathFor(project))
	return nil
}

// readPassword reads a password from the terminal without echoing it.
func readPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	var buf [256]byte
	n, err := os.Stdin.Read(buf[:])
	if err != nil && n == 0 {
		return "", fmt.Errorf("read password: %w", err)
	}
	fmt.Fprintln(os.Stderr)
	return strings.TrimSpace(string(buf[:n])), nil
}

// Key manages SSH public keys from the command line. This is also how the very
// first key gets registered, before the web interface exists.
func Key(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dogit key <add|list|remove>")
	}
	switch args[0] {
	case "add":
		return addKey(ctx, args[1:])
	case "list", "ls":
		return listKeys(ctx, args[1:])
	case "remove", "rm":
		return removeKey(ctx, args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func addKey(ctx context.Context, args []string) error {
	fs := newFlagSet("key add")
	username := fs.String("username", "", "user the key belongs to")
	title := fs.String("title", "", "label for the key, e.g. laptop")
	keyFile := fs.String("file", "", "path to a .pub file; omit to read stdin")

	if err := parse(fs, args); err != nil {
		return err
	}
	if *username == "" {
		return fmt.Errorf("--username is required")
	}

	input, err := readKeyInput(*keyFile)
	if err != nil {
		return err
	}
	parsed, err := auth.ParsePublicKey(input)
	if err != nil {
		return err
	}

	a, err := app.New(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	user, err := a.Store.Users().ByUsername(ctx, *username)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("user %q does not exist", *username)
	}
	if err != nil {
		return err
	}

	label := *title
	if label == "" {
		label = parsed.Comment
	}

	key := &models.SSHKey{
		UserID:      user.ID,
		Title:       label,
		Fingerprint: parsed.Fingerprint,
		PublicKey:   parsed.PEM,
	}
	if err := a.Store.SSHKeys().Create(ctx, key); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "added key %s for %s\n", parsed.Fingerprint, user.Username)
	return nil
}

func listKeys(ctx context.Context, args []string) error {
	fs := newFlagSet("key list")
	username := fs.String("username", "", "user whose keys to list")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *username == "" {
		return fmt.Errorf("--username is required")
	}

	a, err := app.New(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	user, err := a.Store.Users().ByUsername(ctx, *username)
	if err != nil {
		return err
	}
	keys, err := a.Store.SSHKeys().ListByUser(ctx, user.ID)
	if err != nil {
		return err
	}
	for _, k := range keys {
		fmt.Fprintf(os.Stdout, "%s\t%s\t%s\n", k.Fingerprint, k.Title, k.PublicKey)
	}
	return nil
}

func removeKey(ctx context.Context, args []string) error {
	fs := newFlagSet("key remove")
	username := fs.String("username", "", "user the key belongs to")
	fingerprint := fs.String("fingerprint", "", "fingerprint of the key to remove")

	if err := parse(fs, args); err != nil {
		return err
	}
	if *username == "" || *fingerprint == "" {
		return fmt.Errorf("--username and --fingerprint are required")
	}

	a, err := app.New(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	user, err := a.Store.Users().ByUsername(ctx, *username)
	if err != nil {
		return err
	}
	keys, err := a.Store.SSHKeys().ListByUser(ctx, user.ID)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if k.Fingerprint != *fingerprint {
			continue
		}
		if err := a.Store.SSHKeys().Delete(ctx, k.ID, user.ID); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "removed key %s\n", *fingerprint)
		return nil
	}
	return fmt.Errorf("no such key: %s", *fingerprint)
}

func readKeyInput(path string) (string, error) {
	if path == "" {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<16))
		if err != nil {
			return "", fmt.Errorf("read public key from stdin: %w", err)
		}
		return string(data), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(data), nil
}
