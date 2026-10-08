package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// The core, as this module sees it.
//
// A module authenticates as itself and is handed a token at registration. Every
// request after that carries that token and nothing else: no administrator's
// credentials are stored anywhere in this process, and the deployment rights a
// cluster grants are the module's own.
type coreClient struct {
	baseURL string
	token   string

	// databaseURL arrives once, with the registration, and is kept here for the
	// lifetime of the process. The core does not store it either.
	//
	// Assembled here rather than handed over: the core gives the parts of a database —
	// host, port, name, user, password — and what this module opens a connection with is
	// this module's business. A module wanting `postgres://…` and one wanting a
	// keyword/value DSN start from the same six facts, and a core that picked one would be
	// picking on the module's behalf.
	databaseURL string

	// history is this module's own record of what it deployed. Nil when the database
	// could not be reached, which is a state this module can work in and must say so
	// about rather than refuse to deploy.
	history History
}

// registrationAnswer is what the core says when a module introduces itself.
type registrationAnswer struct {
	Integration json.RawMessage `json:"integration"`
	Token       string          `json:"token"`
	// Database is the parts of one database, not a connection string. See databaseParts.
	Database          *databaseParts `json:"database"`
	HeartbeatInterval string         `json:"heartbeat_interval"`
}

// register introduces this module, or introduces it again.
func (c *coreClient) register(ctx context.Context, token, name, endpoint string) (string, error) {
	body := map[string]any{
		"kind":     deployKind,
		"name":     name,
		"endpoint": endpoint,
		"manifest": manifest(),
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v1/modules/register", bytes.NewReader(mustMarshal(body)))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	data, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the core said %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}

	var answer registrationAnswer
	if err := json.Unmarshal(data, &answer); err != nil {
		return "", fmt.Errorf("the core's answer could not be read: %w", err)
	}
	if answer.Database != nil && c.databaseURL == "" {
		assembled, err := answer.Database.connectionString()
		if err != nil {
			return "", err
		}
		c.databaseURL = assembled
	}
	return answer.Token, nil
}

// databaseParts is one database, as the core describes it.
//
// Keys are the core's own words for the parts and are written out rather than taken as a map, so
// that a core which renames or drops one stops compiling here instead of handing this module a
// URL with an empty user in it.
type databaseParts struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Role    string `json:"role"`
	Payload struct {
		Host     string `json:"host"`
		Port     string `json:"port"`
		Database string `json:"database_name"`
		User     string `json:"username"`
		Password string `json:"password"`
	} `json:"payload"`
}

// connectionString turns the parts into what this module opens its history store with.
//
// A keyword/value DSN rather than a URL, because a password with a `@` in it is legal in a
// database and has to be escaped in a URL; escaping it wrong is a connection refused with a
// message about authentication, on the one database whose whole purpose is to be reached.
//
// Every part is required and said so by name. A half-filled DSN is the same string as an empty
// one to the driver, and the resulting error names a socket rather than a missing field.
func (d *databaseParts) connectionString() (string, error) {
	missing := []string{}
	for _, part := range []struct{ name, value string }{
		{"host", d.Payload.Host},
		{"port", d.Payload.Port},
		{"database", d.Payload.Database},
		{"user", d.Payload.User},
		{"password", d.Payload.Password},
	} {
		if strings.TrimSpace(part.value) == "" {
			missing = append(missing, part.name)
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("the core described a database with no %s; a connection string "+
			"missing any of these fails at connect time with an error about a socket rather "+
			"than about the field that was left out", strings.Join(missing, ", "))
	}
	return fmt.Sprintf("host=%s port=%s dbname=%s user=%s password=%s sslmode=disable",
		d.Payload.Host, d.Payload.Port, d.Payload.Database, d.Payload.User, d.Payload.Password), nil
}

// settings asks the core what a project has configured for this module.
//
// The core resolves the inheritance and hands over what applies, because the levels —
// the instance, the group, the project — are the core's business and not this
// module's. A module that worked them out for itself would be a second, disagreeing
// answer to "which settings apply here".
func (c *coreClient) settings(ctx context.Context, project string) (map[string]any, error) {
	path := "/api/v1/module/settings"
	if project != "" {
		path += "?project=" + project
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	var answer struct {
		Effective map[string]any `json:"effective"`
	}
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	return answer.Effective, nil
}

// heartbeat keeps this module marked online.
func heartbeat(ctx context.Context, core *coreClient, every time.Duration, reRegister func() error) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		// Re-registering first when the token has stopped working is what keeps a
		// module online across a core restart: the old token is refused, and a module
		// that only complained would sit there saying it is offline forever.
		if err := core.heartbeat(ctx); err != nil {
			log.Printf("module-deploy: heartbeat failed: %v", err)
			if err := reRegister(); err != nil {
				log.Printf("module-deploy: re-registration failed: %v", err)
			}
		}
	}
}

func (c *coreClient) heartbeat(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v1/module/heartbeat", bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.token)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("the core no longer recognises this module")
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("the core said %d", response.StatusCode)
	}
	return nil
}

func mustMarshal(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}
