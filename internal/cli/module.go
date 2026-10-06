package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/app"
	"github.com/ewolf/dogit/internal/auth"
	"github.com/ewolf/dogit/internal/store"
)

// Module administers the module system: the instance tokens that let modules
// register, and the list of what is currently registered.
func Module(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dogit module <token|list|status|routes>")
	}

	switch args[0] {
	case "token", "tokens":
		return moduleToken(ctx, args[1:])
	case "list", "ls":
		return moduleList(ctx)
	case "status":
		return moduleStatus(ctx, args[1:])
	case "routes":
		return moduleRoutes(ctx, args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func moduleToken(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dogit module token <create|list|revoke>")
	}

	switch args[0] {
	case "create", "add":
		return createModuleToken(ctx, args[1:])
	case "list", "ls":
		return listModuleTokens(ctx)
	case "revoke", "rm":
		return revokeModuleToken(ctx, args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

// createModuleToken mints an instance token.
//
// The plaintext is printed once and never stored: the database keeps only its
// hash, so a leaked table cannot be turned into a working registration token.
func createModuleToken(ctx context.Context, args []string) error {
	fs := newFlagSet("module token create")
	name := fs.String("name", "", "what the token is for, e.g. registry-docker")
	description := fs.String("description", "", "free text shown in the list")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("--name is required")
	}

	plaintext, hash, err := auth.GenerateToken()
	if err != nil {
		return err
	}

	a, err := app.New(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	token, err := a.Store.ModuleTokens().Create(ctx, *name, *description, hash)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "created module token %s\n", token.Name)
	fmt.Fprintf(os.Stdout, "  id:   %s\n", token.ID)
	fmt.Fprintf(os.Stdout, "  token: %s\n", plaintext)
	fmt.Fprintln(os.Stdout, "\nThis value is shown once. Store it in the module's secret.")
	return nil
}

func listModuleTokens(ctx context.Context) error {
	a, err := app.New(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	tokens, err := a.Store.ModuleTokens().List(ctx)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		fmt.Fprintln(os.Stdout, "no module tokens")
		return nil
	}
	for _, token := range tokens {
		state := "active"
		if token.RevokedAt != nil {
			state = "revoked"
		}
		fmt.Fprintf(os.Stdout, "%s\t%s\t%s\t%s\n", token.ID, state, token.Name, token.CreatedAt.Format(time.RFC3339))
	}
	return nil
}

func revokeModuleToken(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dogit module token revoke <id>")
	}
	id, err := uuid.Parse(args[0])
	if err != nil {
		return fmt.Errorf("invalid token id: %w", err)
	}

	a, err := app.New(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	if err := a.Store.ModuleTokens().Revoke(ctx, id); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "revoked module token %s\n", id)
	return nil
}

func moduleList(ctx context.Context) error {
	a, err := app.New(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	integrations, err := a.Store.Integrations().List(ctx)
	if err != nil {
		return err
	}
	if len(integrations) == 0 {
		fmt.Fprintln(os.Stdout, "no modules registered")
		return nil
	}

	for _, integration := range integrations {
		lastSeen := "never"
		if integration.LastSeenAt != nil {
			lastSeen = integration.LastSeenAt.Format(time.RFC3339)
		}
		fmt.Fprintf(os.Stdout, "%-22s %-18s %-9s %-8s %s\n",
			integration.Kind, integration.Name, integration.Status,
			integration.ModuleVersion, lastSeen)
		fmt.Fprintf(os.Stdout, "  id: %s\n  endpoint: %s\n", integration.ID, integration.Endpoint)
	}
	return nil
}

// moduleStatus reports whether a module kind is present, enabled and online, and
// prints the settings it would run a job with. It is the command a job script
// calls to learn where to point itself.
func moduleStatus(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dogit module status <kind> [project-path]")
	}

	a, err := app.New(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	kind := args[0]
	integration, err := a.Store.Integrations().ByKind(ctx, kind)
	if errors.Is(err, store.ErrNotFound) {
		fmt.Fprintf(os.Stdout, "module %s: not installed\n", kind)
		return exitCode(1)
	}
	if err != nil {
		return err
	}

	var projectID *uuid.UUID
	if len(args) > 1 {
		project, err := a.Store.Projects().ByPath(ctx, strings.Trim(args[1], "/"))
		if err != nil {
			return fmt.Errorf("project %q not found", args[1])
		}
		projectID = &project.ID
	}

	settings, err := a.Store.Integrations().SettingsFor(ctx, integration.ID, nil, projectID, integration.Capabilities.Settings)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "kind:     %s\n", integration.Kind)
	fmt.Fprintf(os.Stdout, "name:     %s\n", integration.Name)
	fmt.Fprintf(os.Stdout, "endpoint: %s\n", integration.Endpoint)
	fmt.Fprintf(os.Stdout, "status:   %s\n", integration.Status)
	fmt.Fprintf(os.Stdout, "scopes:   %s\n", strings.Join(integration.Capabilities.Scopes, " "))
	if len(settings) > 0 {
		fmt.Fprintln(os.Stdout, "settings:")
		for key, raw := range settings {
			fmt.Fprintf(os.Stdout, "  %s=%s\n", key, string(raw))
		}
	}

	// Scripts consume this line, so it is machine-readable on its own.
	compact, err := json.Marshal(map[string]any{
		"kind":     integration.Kind,
		"name":     integration.Name,
		"endpoint": integration.Endpoint,
		"status":   integration.Status,
		"settings": settings,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "json: %s\n", compact)
	return nil
}
