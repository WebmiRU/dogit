// Command module-notify-telegram sends the instance's notifications to Telegram.
//
// It is a module rather than part of the core for the same reason every other
// module is one: the core has no idea Telegram exists, and the day somebody wants
// notifications somewhere else — a chat, a webhook, an email — that is another
// module rather than another branch in the core.
//
// What it sends is decided by the core, which is the only place that knows what
// happened. The module decides how it looks and where it goes, and nothing else.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultCoreURL  = "http://app:8080"
	defaultEndpoint = "http://module-notify:8093"
	defaultListen   = ":8093"
	defaultName     = "telegram"
	notifyKind      = "notify:telegram"

	// Telegram will not accept a message longer than this, so a long one is
	// trimmed with its end marked rather than refused: a notification that does not
	// arrive is worse than one that arrives shortened.
	maxMessage = 4096
)

func main() {
	cfg := config{}

	flag.StringVar(&cfg.coreURL, "core", envOr("DOGIT_CORE_URL", defaultCoreURL),
		"base URL of the dogit core")
	flag.StringVar(&cfg.registrationToken, "registration-token", os.Getenv("DOGIT_MODULE_TOKEN"),
		"instance token created with: dogit module token create")
	flag.StringVar(&cfg.endpoint, "endpoint", envOr("DOGIT_MODULE_ENDPOINT", defaultEndpoint),
		"address the core should use to reach this module")
	flag.StringVar(&cfg.name, "name", envOr("DOGIT_MODULE_NAME", defaultName),
		"module name, unique per kind")
	flag.StringVar(&cfg.listen, "listen", envOr("DOGIT_MODULE_LISTEN", defaultListen),
		"address this module listens on")
	flag.DurationVar(&cfg.interval, "heartbeat", 60*time.Second, "heartbeat interval")
	flag.Parse()

	if cfg.registrationToken == "" {
		log.Fatal("module-notify: a registration token is required (DOGIT_MODULE_TOKEN)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	core := &coreClient{baseURL: strings.TrimRight(cfg.coreURL, "/")}

	// Registration comes before anything else, because the settings that say where
	// messages go are the module's own and are read through the core.
	token, err := core.register(ctx, cfg.registrationToken, cfg.name, cfg.endpoint, manifest())
	if err != nil {
		log.Fatalf("module-notify: registration failed: %v", err)
	}
	core.token = token
	log.Printf("module-notify: registered, heartbeat every %s", cfg.interval)

	register := func() error {
		token, err := core.register(ctx, cfg.registrationToken, cfg.name, cfg.endpoint, manifest())
		if err != nil {
			return err
		}
		core.token = token
		log.Printf("module-notify: re-registered")
		return nil
	}

	// A deployment knows its bot token; the core does not. Writing it as a setting
	// is what puts the secret somewhere it can be changed from the interface, and it
	// is the module's own secret to hand over rather than something to be asked for.
	if _, err := seed(ctx, core); err != nil {
		log.Printf("module-notify: could not write its own settings: %v", err)
	}

	settings, err := core.settings(ctx)
	if err != nil {
		log.Printf("module-notify: could not read settings yet: %v", err)
	}
	if err := verifyChat(ctx, settings); err != nil {
		// Not fatal: a chat that has not been set up yet is a thing an administrator
		// fixes from the page, and a module that refuses to start would show a
		// registration failure instead of the reason.
		log.Printf("module-notify: not sending yet: %v", err)
	}

	go heartbeat(ctx, core, cfg.interval, register)
	go poll(ctx, core, settings)

	server := &http.Server{
		Addr:              cfg.listen,
		Handler:           ownEndpoints(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("module-notify: listening on %s", cfg.listen)
		_ = server.ListenAndServe()
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	<-ctx.Done()
	log.Printf("module-notify: stopped")
}

type config struct {
	coreURL           string
	registrationToken string
	endpoint          string
	name              string
	listen            string
	interval          time.Duration
}

// manifest is what this module says it does.
//
// The wording is the operator's: "which chat" is a question only somebody looking
// at Telegram can answer, and the core renders this rather than inventing fields.
func manifest() map[string]any {
	return map[string]any{
		"version":     "0.1.0",
		"description": "Sends notifications to a Telegram chat",
		"scopes":      []string{},

		"settings": []map[string]any{
			{
				"key":         "bot_token",
				"label":       "Bot token",
				"type":        "string",
				"secret":      true,
				"description": "From @BotFather. Never returned by the core once written.",
			},
			{
				"key":         "chat_id",
				"label":       "Chat id",
				"type":        "string",
				"description": "The group the bot was added to. A negative number is a group; a positive one is a person.",
			},
			{
				"key":         "thread_id",
				"label":       "Topic id",
				"type":        "string",
				"description": "Optional. For a group with topics on, which one these go to.",
			},
			{
				"key":         "events",
				"label":       "What to send",
				"type":        "enum",
				"options":     []string{"everything", "pipelines", "merge_requests", "none"},
				"default":     "everything",
				"description": "Which events are worth a message. Testing messages are sent whatever this says.",
			},
			{
				"key":         "send_test",
				"label":       "Test button",
				"type":        "bool",
				"default":     true,
				"description": "Show a button on the module page that sends one message, so the settings can be checked before anything is relied on.",
			},
		},
	}
}

// notification is one thing that happened, as the core describes it.
type notification struct {
	ID     int64          `json:"id"`
	Kind   string         `json:"kind"`
	Text   string         `json:"text"`
	URL    string         `json:"url,omitempty"`
	Levels []string       `json:"levels,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
}

// seed writes what the deployment knows and the module has not been told yet.
//
// The bot token comes from the environment because a secret in a settings page is a
// secret somebody pastes into a chat. The chat is found by looking at what the bot
// has been sent: a bot cannot message a group it has not been added to, so the
// group has to have spoken first, and the only honest way to find it is to read
// what Telegram says.
func seed(ctx context.Context, core *coreClient) (bool, error) {
	_ = core.integrationID
	values := map[string]any{}

	if token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")); token != "" {
		values["bot_token"] = token
	}

	chatID := strings.TrimSpace(os.Getenv("TELEGRAM_CHAT_ID"))
	if chatID == "" {
		discovered, err := discoverChat(ctx, envOr("TELEGRAM_BOT_TOKEN", ""), envOr("TELEGRAM_CHAT_NAME", ""))
		if err != nil {
			log.Printf("module-notify: no chat found yet: %v", err)
		} else if discovered != "" {
			log.Printf("module-notify: found the chat %s", discovered)
			chatID = discovered
		}
	}
	if chatID != "" {
		values["chat_id"] = chatID
	}

	if len(values) == 0 {
		return false, errors.New("nothing to seed")
	}

	body, err := json.Marshal(map[string]any{"values": values})
	if err != nil {
		return false, err
	}

	// Written through the module's own endpoint: this is the module recording what
	// it was told, not an administrator configuring it.
	request, err := http.NewRequestWithContext(ctx, http.MethodPut,
		core.baseURL+"/api/v1/module/settings",
		strings.NewReader(string(body)))
	if err != nil {
		return false, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+core.token)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("core said %d", response.StatusCode)
	}
	return true, nil
}

// discoverChat finds the chat the bot was added to.
//
// Telegram has no way to ask "which groups am I in". A bot can only see a group
// after somebody has written in it, so this reads what the bot has been sent and
// looks for the group by name. Nothing is written anywhere: reading updates does
// not consume them, and the next poll of this module's messages is unaffected.
func discoverChat(ctx context.Context, botToken, wanted string) (string, error) {
	if botToken == "" {
		return "", errors.New("no bot token")
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.telegram.org/bot"+botToken+"/getUpdates?limit=100", nil)
	if err != nil {
		return "", err
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	var answer struct {
		OK     bool `json:"ok"`
		Result []struct {
			Message struct {
				Chat struct {
					ID    int64  `json:"id"`
					Type  string `json:"type"`
					Title string `json:"title"`
				} `json:"chat"`
			} `json:"message"`
		} `json:"result"`
	}
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		return "", err
	}
	if !answer.OK {
		return "", errors.New("telegram refused to say what the bot has received")
	}

	// Newest first, so the group somebody has just written in wins over an old one
	// of the same name.
	for index := len(answer.Result) - 1; index >= 0; index-- {
		chat := answer.Result[index].Message.Chat
		if chat.Type != "group" && chat.Type != "supergroup" {
			continue
		}
		if wanted == "" || strings.EqualFold(chat.Title, wanted) {
			return strconv.FormatInt(chat.ID, 10), nil
		}
	}
	return "", errors.New("no group among what the bot has received — write something in it once")
}

// poll watches for notifications.
//
// It asks the core for what it has not sent rather than subscribing. A module
// that has been down for an hour should deliver the hour, not quietly start
// again from now: a notification system that loses what happened while it was
// broken is worse than no notification system, because it is trusted.
func poll(ctx context.Context, core *coreClient, settings map[string]any) {
	cursor := int64(0)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		answer, err := core.notifications(ctx, cursor)
		if err != nil {
			if errors.Is(err, errUnauthorized) {
				continue
			}
			log.Printf("module-notify: asking for notifications: %v", err)
			continue
		}

		for _, note := range answer.Notifications {
			// The test button sends whether or not the module is configured to send
			// anything: somebody is checking that it works, and filtering their check
			// by their own settings is a small betrayal.
			if note.Kind != "test" && !wanted(note, settings) {
				cursor = max64(cursor, note.ID)
				continue
			}
			if err := send(ctx, settings, note); err != nil {
				log.Printf("module-notify: could not send: %v", err)
				continue
			}
			cursor = max64(cursor, note.ID)
		}

		// Remember where we were even on a run with nothing to send, so a restart
		// does not ask for everything since the beginning of time.
		if answer.Cursor > cursor {
			cursor = answer.Cursor
		}
		if err := core.acknowledge(ctx, cursor); err != nil {
			// Progress is not recorded. The next poll asks again and re-sends what was
			// already delivered, which is better than skipping a message because a
			// bookkeeping call failed: a duplicate is an annoyance, a gap is a lie.
			log.Printf("module-notify: could not record progress: %v", err)
		}
	}
}

func wanted(note notification, settings map[string]any) bool {
	switch eventFilter(settings) {
	case "none":
		return false
	case "pipelines":
		return strings.HasPrefix(note.Kind, "pipeline") || strings.HasPrefix(note.Kind, "job")
	case "merge_requests":
		return strings.HasPrefix(note.Kind, "merge_request")
	default:
		return true
	}
}

func eventFilter(settings map[string]any) string {
	if value, ok := settings["events"].(string); ok && value != "" {
		return value
	}
	return "everything"
}

// botTokenOf says which token to call Telegram with.
//
// The core keeps the token as a secret, so reading it back from the settings page
// returns a mask — a module that took the stored value would send every message as
// an unauthenticated stranger and be told, correctly, that it does not exist. The
// deployment's own environment is where the token came from, so that is what is
// used; the stored value is only a fallback, and only when it is not the mask.
func botTokenOf(settings map[string]any) string {
	if token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")); token != "" {
		return token
	}
	stored, _ := settings["bot_token"].(string)
	if stored == redactedSetting {
		return ""
	}
	return strings.TrimSpace(stored)
}

// redactedSetting is what the core sends instead of a secret.
const redactedSetting = "********"

// send delivers one notification.
//
// The text is HTML with the parts that came from a branch or a commit name escaped,
// because those are things people push: an unescaped "&" in a tag name is a
// notification that fails to send with a message about parse errors.
func send(ctx context.Context, settings map[string]any, note notification) error {
	botToken := botTokenOf(settings)
	chatID, _ := settings["chat_id"].(string)
	if botToken == "" || chatID == "" {
		return errors.New("the bot token or the chat id is not set")
	}

	payload := map[string]any{
		"chat_id":    chatID,
		"text":       render(note),
		"parse_mode": "HTML",
		// A link is only worth having when there is somewhere to go.
		"disable_web_page_preview": true,
	}
	if thread, _ := settings["thread_id"].(string); thread != "" {
		if number, err := strconv.ParseInt(thread, 10, 64); err == nil {
			payload["message_thread_id"] = number
		}
	}
	if note.URL != "" {
		// The link is what makes a notification actionable: the text says what
		// happened and the button says where to go and look at it.
		payload["reply_markup"] = map[string]any{
			"inline_keyboard": [][]map[string]any{{
				{"text": "Open in dogit", "url": absoluteURL(note.URL)},
			}},
		}
	}

	return callTelegram(ctx, botToken, "sendMessage", payload)
}

func render(note notification) string {
	var out strings.Builder
	out.WriteString(escapeHTML(note.Text))
	if len(out.String()) > maxMessage {
		trimmed := []rune(out.String())
		out.Reset()
		out.WriteString(string(trimmed[:maxMessage-64]))
		out.WriteString("\n…")
	}
	return out.String()
}

// escapeHTML keeps a branch name or a commit message from breaking the message.
func escapeHTML(text string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	)
	return replacer.Replace(text)
}

// absoluteURL makes a link from the instance, because a Telegram client is not on
// this network and a relative link opens nothing.
func absoluteURL(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return "https://" + envOr("DOGIT_PUBLIC_HOST", "localhost") + strings.TrimPrefix(path, "/")
}

// callTelegram is one call to the Bot API.
func callTelegram(ctx context.Context, botToken, method string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.telegram.org/bot"+botToken+"/"+method, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		var problem struct {
			Description string `json:"description"`
		}
		_ = json.NewDecoder(response.Body).Decode(&problem)
		if problem.Description == "" {
			problem.Description = response.Status
		}
		return fmt.Errorf("telegram said %d: %s", response.StatusCode, problem.Description)
	}
	return nil
}

// verifyChat checks that the bot can actually post, before anything is relied on.
func verifyChat(ctx context.Context, settings map[string]any) error {
	botToken := botTokenOf(settings)
	chatID, _ := settings["chat_id"].(string)
	if botToken == "" || chatID == "" {
		return errors.New("the bot token or the chat id is not set yet")
	}

	// getChat does not post anything, so checking where messages will land does not
	// itself leave a test message behind.
	var answer struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
			Type  string `json:"type"`
		} `json:"result"`
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.telegram.org/bot"+botToken+"/getChat?chat_id="+url.QueryEscape(chatID), nil)
	if err != nil {
		return err
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		return err
	}
	if !answer.OK {
		return fmt.Errorf("telegram: %s", answer.Description)
	}

	log.Printf("module-notify: messages go to %s (%s, id %d)",
		answer.Result.Title, answer.Result.Type, answer.Result.ID)
	return nil
}

func ownEndpoints() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/-/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("/-/ready", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ready")
	})
	mux.HandleFunc("/module/manifest", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, manifest())
	})
	return mux
}

// coreClient talks to the dogit core.
type coreClient struct {
	baseURL string
	token   string
	// integrationID is this module's own row, which is where its settings live.
	integrationID string
}

var errUnauthorized = errors.New("unauthorized")

// post calls the core as the module itself.
func (c *coreClient) post(ctx context.Context, path string, body any, out any) error {
	return c.postAs(ctx, path, body, c.token, out)
}

// postAs calls the core with a particular credential. Registration is the one
// place that uses a different one: the module has no token yet, and presents the
// instance token an administrator created for it.
func (c *coreClient) postAs(ctx context.Context, path string, body any, token string, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path,
		strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusUnauthorized {
		return errUnauthorized
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("core said %d", response.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(out)
}

func (c *coreClient) register(ctx context.Context, token, name, endpoint string, man map[string]any) (string, error) {
	var answer struct {
		Token string `json:"token"`
	}
	// With the instance token, not with c.token: the module has none yet.
	if err := c.postAs(ctx, "/api/v1/modules/register", map[string]any{
		"kind": notifyKind, "name": name, "endpoint": endpoint, "manifest": man,
	}, token, &answer); err != nil {
		return "", err
	}
	if answer.Token == "" {
		return "", errors.New("the core returned no module token")
	}
	return answer.Token, nil
}

func (c *coreClient) settings(ctx context.Context) (map[string]any, error) {
	var answer struct {
		Effective map[string]any `json:"effective"`
	}
	if err := c.get(ctx, "/api/v1/module/settings", &answer); err != nil {
		return nil, err
	}
	return answer.Effective, nil
}

// notifications is what the core has that this module has not sent yet.
func (c *coreClient) notifications(ctx context.Context, after int64) (struct {
	Notifications []notification `json:"notifications"`
	Cursor        int64          `json:"cursor"`
}, error) {
	var answer struct {
		Notifications []notification `json:"notifications"`
		Cursor        int64          `json:"cursor"`
	}
	if err := c.post(ctx, "/api/v1/module/notifications",
		map[string]any{"after": after}, &answer); err != nil {
		return answer, err
	}
	return answer, nil
}

func (c *coreClient) acknowledge(ctx context.Context, cursor int64) error {
	return c.post(ctx, "/api/v1/module/notifications/ack",
		map[string]any{"cursor": cursor}, nil)
}

func (c *coreClient) get(ctx context.Context, path string, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("core said %d", response.StatusCode)
	}
	return json.NewDecoder(response.Body).Decode(out)
}

func (c *coreClient) beat(ctx context.Context, stats map[string]any) error {
	return c.post(ctx, "/api/v1/module/heartbeat", map[string]any{"stats": stats}, nil)
}

var startedAt = time.Now()

// stats is what this module can say about itself.
//
// Almost nothing: it is a process that makes occasional requests and has no disk
// of its own worth reporting. Anything it did not measure is left out rather than
// reported as zero.
func stats() map[string]any {
	return map[string]any{
		"uptime_seconds": int64(time.Since(startedAt).Seconds()),
		"extra": map[string]any{
			"transport": "telegram",
		},
	}
}

func heartbeat(ctx context.Context, core *coreClient, interval time.Duration, register func() error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := core.beat(ctx, stats())
			if err == nil {
				continue
			}
			if !errors.Is(err, errUnauthorized) {
				log.Printf("module-notify: heartbeat failed: %v", err)
				continue
			}
			log.Printf("module-notify: token rejected, re-registering")
			if err := register(); err != nil {
				log.Printf("module-notify: re-registration failed: %v", err)
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
