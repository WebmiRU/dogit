package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/app"
	"github.com/ewolf/dogit/internal/auth"
	"github.com/ewolf/dogit/internal/models"
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
	expires := fs.String("expires", "",
		"when the token stops working, e.g. 24h or 30d. Empty means it does not end, which "+
			"is the right answer for a token kept somewhere safe")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("--name is required")
	}

	// A date that cannot be read is refused here rather than stored. A token with a date
	// nobody could parse would read as perpetual to everyone except the one who meant it to
	// end, which is the one reading that matters.
	var expiresAt *time.Time
	if text := strings.TrimSpace(*expires); text != "" {
		after, err := parseDuration(text)
		if err != nil {
			return fmt.Errorf("--expires: %w", err)
		}
		moment := time.Now().Add(after)
		expiresAt = &moment
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

	token, err := a.Store.ModuleTokens().Create(ctx, *name, *description, hash, expiresAt)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "created module token %s\n", token.Name)
	fmt.Fprintf(os.Stdout, "  id:   %s\n", token.ID)
	fmt.Fprintf(os.Stdout, "  token: %s\n", plaintext)
	fmt.Fprintln(os.Stdout, "\nThis value is shown once. Store it in the module's secret.")
	return nil
}

// parseDuration reads what somebody wrote on a command line: 30s, 24h, 7d, or something Go
// understands on its own.
//
// The day suffix is here because a token's life is counted in days by everybody who thinks
// about tokens, and "604800s" is what you write when you have given up on the interface.
func parseDuration(text string) (time.Duration, error) {
	if strings.HasSuffix(text, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(text, "d"))
		if err != nil {
			return 0, fmt.Errorf("%q is not a number of days", text)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(text)
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
		// The column an operator actually needs is the one that says what a token is doing: an
		// unused one is an invitation, a bound one belongs to a module, and an expired one is
		// neither.
		state := "unused — this will register a module when presented"
		switch {
		case token.RevokedAt != nil:
			state = "revoked"
		case token.Bound():
			state = "in use by a module"
		case token.Expired(time.Now()):
			state = "ended " + token.ExpiresAt.Format("2 January 2006")
		}
		fmt.Fprintf(os.Stdout, "%s\t%s\t%s\t%s\n",
			token.ID, state, token.Name, token.CreatedAt.Format(time.RFC3339))
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

	// Both outcomes are reported, because they are different acts with different consequences and
	// an operator who cannot tell which happened has to go and look.
	removed, err := a.Store.ModuleTokens().Revoke(ctx, id)
	if err != nil {
		return err
	}
	if removed == nil {
		fmt.Fprintf(os.Stdout, "revoked module token %s — no module was using it\n", id)
		return nil
	}
	fmt.Fprintf(os.Stdout,
		"revoked module token %s and removed %s/%s: the token was the module's only credential, "+
			"so the module is gone with it. Registering it again needs a new token.\n",
		id, removed.Kind, removed.Name)
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

	// Every module of this kind, not the oldest one.
	//
	// The command takes a kind, so a kind with two modules installed has two answers, and
	// printing one of them is how the other stayed invisible for as long as it did. With one
	// module the output is byte for byte what it always was; with several there is one block
	// and one machine-readable line per module.
	integrations, err := a.Store.Integrations().ByKindAll(ctx, kind)
	if err != nil {
		return err
	}
	usable := make([]*models.Integration, 0, len(integrations))
	for _, candidate := range integrations {
		if candidate.Enabled {
			usable = append(usable, candidate)
		}
	}
	if len(usable) == 0 {
		fmt.Fprintf(os.Stdout, "module %s: not installed\n", kind)
		return exitCode(1)
	}
	if len(usable) > 1 {
		names := make([]string, 0, len(usable))
		for _, candidate := range usable {
			names = append(names, candidate.Name)
		}
		fmt.Fprintf(os.Stdout, "module %s: %d are installed (%s)\n\n",
			kind, len(usable), strings.Join(names, ", "))
	}

	var projectID *uuid.UUID
	if len(args) > 1 {
		project, err := a.Store.Projects().ByPath(ctx, strings.Trim(args[1], "/"))
		if err != nil {
			return fmt.Errorf("project %q not found", args[1])
		}
		projectID = &project.ID
	}

	for i, integration := range usable {
		if i > 0 {
			fmt.Fprintln(os.Stdout)
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

		// Scripts consume this line, so it is machine-readable on its own. One per module,
		// which is the only shape that can describe more than one.
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
	}
	return nil
}
