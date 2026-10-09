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

	"github.com/ewolf/dogit/internal/coreerr"
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
//
// It carries no credentials, and that is the shape of things now: a module is configured with
// settings, and the ones it marks secret are its own. Nothing is handed over once at
// registration, because a credential given once and kept in a file is a credential that
// cannot be rotated — and the administrator is the one who has the database.
type registrationAnswer struct {
	Integration       json.RawMessage `json:"integration"`
	Token             string          `json:"token"`
	HeartbeatInterval string          `json:"heartbeat_interval"`
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
		return "", coreerr.Refusal(response)
	}

	var answer registrationAnswer
	if err := json.Unmarshal(data, &answer); err != nil {
		return "", fmt.Errorf("the core's answer could not be read: %w", err)
	}
	return answer.Token, nil
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
		return coreerr.Refusal(response)
	}
	return nil
}

func mustMarshal(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}

// databaseSetting is where an administrator said this module's history lives.
//
// Asked of the core without a project, because this is a setting about the module and not
// about anything a project deploys: a project scope on it would mean a project's
// configuration choosing where a module keeps its own records, and two projects would then be
// able to point one module at two databases.
//
// Empty is an ordinary answer rather than a failure — the core refuses to let this module
// register without it, so an empty value here means somebody removed it afterwards — and the
// caller says what is lost rather than refusing to deploy.
func (c *coreClient) databaseSetting(ctx context.Context) string {
	answer, err := c.settings(ctx, "")
	if err != nil {
		return ""
	}
	text, isText := answer[databaseSettingKey].(string)
	if !isText {
		return ""
	}
	return strings.TrimSpace(text)
}

// databaseSettingKey is the name the manifest declares the setting under. Written out here as
// well as in the manifest because the two are used from opposite ends and a rename of one
// without the other is a module that asks for a setting it cannot read.
const databaseSettingKey = "database_url"
