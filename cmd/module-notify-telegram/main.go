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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ewolf/dogit/internal/notifymarkup"
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
	// Not checking anything here on purpose. Which chats are configured changes over
	// time and is answered by the notifications page, and a module that refuses to
	// start because one chat is not set up would show a registration failure instead
	// of the reason. A message that cannot be delivered says so in the log and is
	// offered again by the button on the module page.
	if len(settings) == 0 {
		log.Printf("module-notify: no settings yet; nothing will be delivered until a chat is configured")
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

		// Everything below makes one destination. A module pointed at two chats is
		// two rows of these settings, not two settings, and the core keeps them
		// separate so the two messages do not become one.
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
				// What this recipient says when the pipeline had nothing of its own to
				// say. It fills in and it never speaks first: a run whose configuration
				// says nothing is still silent.
				"key":   "default_title",
				"label": "Default title",
				"type":  "string",
				"description": "Used when a pipeline announces itself without writing a title. " +
					"${…} is filled in as usual.",
			},
			{
				"key":   "default_text",
				"label": "Default text",
				"type":  "string",
				"description": "Used when a pipeline announces itself without writing any text. " +
					"Bold, _italic_, `code` and links are understood and translated; " +
					"${…} is filled in as usual.",
			},
		},

		// What a row in the recipients list is, in this module's words: a chat id
		// names one, and the core is told so rather than guessing from a column name.
		"target": map[string]any{
			// The token is deliberately not here. It is the bot, and one bot serves
			// every chat; a repository given a row of its own must never be shown a
			// secret that belongs to the module.
			"settings":    []string{"chat_id", "thread_id", "default_title", "default_text"},
			"identify":    []string{"chat_id", "thread_id"},
			"title":       "Which chats",
			"description": "One row per chat this bot writes to. The same bot can write to as many as you add, and every row is a separate message.",
		},
	}
}

// notification is one thing that happened, as the core describes it.
// notificationAnswer is what the core hands over.
type notificationAnswer struct {
	Notifications []notification `json:"notifications"`
	Cursor        int64          `json:"cursor"`
	// ResumeFrom is where this module was last time it asked.
	ResumeFrom int64 `json:"resume_from"`
}

type notification struct {
	ID     int64          `json:"id"`
	Kind   string         `json:"kind"`
	Text   string         `json:"text"`
	URL    string         `json:"url,omitempty"`
	Levels []string       `json:"levels,omitempty"`
	Data   map[string]any `json:"data,omitempty"`

	// Where this particular message is for. The core can be pointed at more than
	// one chat and says so per record, because the core is what knows which of them
	// this event was meant for.
	TargetID     string         `json:"target_id,omitempty"`
	TargetValues map[string]any `json:"target_values,omitempty"`
}

// recipient is where one record goes: the recipient row the core addressed it to,
// on top of the module's own settings.
//
// The bot is the module and the chat is the recipient, so the token is a property of
// this module and the chat id is a property of the row. That split is why a token
// written once covers every chat the bot writes to, and why a project can point its
// own row at another chat without having to know the token at all.
//
// A record that names no row falls back entirely to the module's settings: a
// message queued before recipients existed names no row, and a queue written an hour
// ago must go to the chat it was meant for even if the settings have changed since.
func recipient(note notification, settings map[string]any) map[string]any {
	out := make(map[string]any, len(settings)+len(note.TargetValues))
	for key, value := range settings {
		out[key] = value
	}
	for key, value := range note.TargetValues {
		out[key] = value
	}
	return out
}

// seed writes what the deployment knows and the module has not been told yet.
//
// The bot token is a setting: it is the bot, and one bot serves every chat. The chat
// is a recipient, because a destination is a row like any other — listed,
// inherited, switched off by a project that does not want it. A deployment that is
// given a chat in its environment gets that one row and no others: adding another is
// somebody's decision, made where they can see what it will do.
//
// The chat is found by looking at what the bot has been sent: a bot cannot message a
// group it has not been added to, so the group has to have spoken first, and the only
// honest way to find it is to read what Telegram says.
func seed(ctx context.Context, core *coreClient) (bool, error) {
	wrote := false

	if token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")); token != "" {
		if _, err := writeSettings(ctx, core, map[string]any{"bot_token": token}); err != nil {
			return wrote, err
		}
		wrote = true
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
	if chatID == "" {
		return wrote, nil
	}

	values := map[string]string{"chat_id": chatID}
	if thread := strings.TrimSpace(os.Getenv("TELEGRAM_THREAD_ID")); thread != "" {
		values["thread_id"] = thread
	}

	body, err := json.Marshal(map[string]any{
		// The name is the chat itself, so a deployment that changes its chat gets a
		// second row an administrator can look at and delete, rather than a row that
		// silently changed what every inherited message goes to.
		"label":  "chat " + chatID,
		"values": values,
	})
	if err != nil {
		return wrote, err
	}

	// Written through the module's own endpoint: this is the module recording what it
	// was told, not an administrator configuring it.
	request, err := http.NewRequestWithContext(ctx, http.MethodPut,
		core.baseURL+"/api/v1/module/targets", bytes.NewReader(body))
	if err != nil {
		return wrote, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+core.token)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return wrote, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return wrote, fmt.Errorf("core said %d", response.StatusCode)
	}
	return true, nil
}

// writeSettings records what the module's own deployment knows.
func writeSettings(ctx context.Context, core *coreClient, values map[string]any) (bool, error) {
	body, err := json.Marshal(map[string]any{"values": values})
	if err != nil {
		return false, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPut,
		core.baseURL+"/api/v1/module/settings", bytes.NewReader(body))
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
	// Starting at zero would mean a redeploy replays the whole queue, which is how a
	// notification channel ends up sending the same build three times in an evening.
	// The core remembers what this module was told, so the first poll asks from there.
	cursor := core.resumeFrom(ctx)
	if cursor > 0 {
		log.Printf("module-notify: resuming after record %d", cursor)
	}

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

		// Every record is sent to the place it names. What is worth saying at all is
		// decided by the pipeline that said it, not here: a module that filtered
		// events would be deciding on the project's behalf, from a list it would have
		// to keep up to date.
		// Everything in the batch goes at once.
		//
		// The recipients are separate places, not steps in a queue: one message to a
		// slow chat must not hold up the same message to a fast one, and which of them
		// arrives first was never something to depend on. Telegram itself is the
		// reason — a few hundred milliseconds per call, and a batch of them in a row
		// is the difference between a notification and a notification from yesterday.
		delivered := deliverAll(ctx, settings, answer.Notifications)

		// The cursor only moves over records that were sent. One that failed is asked
		// for again on the next pass, which repeats it — a duplicate is an annoyance,
		// a gap is a lie.
		for _, note := range answer.Notifications {
			if !delivered[note.ID] {
				break
			}
			cursor = max64(cursor, note.ID)
		}

		// The batch's last id is not proof that the batch was sent: taking it would
		// step over a record whose delivery failed, and a gap is a worse lie than a
		// repeat. The cursor moved only over what went out.
		if err := core.acknowledge(ctx, cursor); err != nil {
			// Progress is not recorded. The next poll asks again and re-sends what was
			// already delivered, which is better than skipping a message because a
			// bookkeeping call failed: a duplicate is an annoyance, a gap is a lie.
			log.Printf("module-notify: could not record progress: %v", err)
		}
	}
}

// botTokenOf says which token to call Telegram with.
//
// The core keeps the token as a secret, so reading it back from the settings page
// returns a mask — a module that took the stored value would send every message as
// an unauthenticated stranger and be told, correctly, that it does not exist. The
// deployment's own environment is where the token came from, so that is what is
// used; the stored value is only a fallback, and only when it is not the mask.
// deliverAll sends a batch at once and says which of them went.
//
// A few at a time rather than all of them: a hundred queued messages would otherwise
// become a hundred simultaneous requests, which is the same as a denial of service
// aimed at ourselves.
func deliverAll(ctx context.Context, settings map[string]any, notes []notification) map[int64]bool {
	const atOnce = 4

	done := make(map[int64]bool, len(notes))
	guard := make(chan struct{}, atOnce)

	var group sync.WaitGroup
	for _, note := range notes {
		group.Add(1)
		go func(note notification) {
			defer group.Done()
			guard <- struct{}{}
			defer func() { <-guard }()

			if err := send(ctx, recipient(note, settings), note); err != nil {
				log.Printf("module-notify: could not send: %v", err)
				return
			}
			done[note.ID] = true
		}(note)
	}
	group.Wait()

	return done
}

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
// send delivers one record to the recipient it names.
//
// The settings it is given are that recipient's, already resolved by the core: this
// module knows how Telegram works and where to send, not who should be told.
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

// render is what one message looks like here.
//
// The title is the pipeline's own when it wrote one: somebody who wrote "The
// deployment failed" in their configuration means that, and this module has no
// business improving on it. Without one it is composed here from the facts, because a
// Telegram message that begins mid-sentence is a message nobody reads.
//
// The body is the shared markup, translated into what Telegram can show — and a mark
// Telegram cannot show is dropped rather than printed, because a message with `**` in
// it reads as a message that arrived broken.
func render(note notification) string {
	blocks := notifymarkup.Parse(note.Text)

	title := titleOf(note)
	if title != "" {
		// Bold, then a blank line: a headline pressed against its own text reads as one
		// long sentence, and the blank line is what makes it a headline.
		var out strings.Builder
		out.WriteString("<b>")
		out.WriteString(escapeHTML(title))
		out.WriteString("</b>")
		if len(blocks) > 0 {
			out.WriteString("\n\n")
		}
		out.WriteString(toTelegramHTML(blocks))

		return trim(out.String())
	}
	return trim(toTelegramHTML(blocks))
}

// toTelegramHTML writes the markup as Telegram's own.
//
// Every mark is opened and closed inside its own span, because Telegram's HTML is not
// a general markup language: an unclosed tag is rejected outright, and a message
// rejected outright is a message nobody sees.
func toTelegramHTML(blocks []notifymarkup.Block) string {
	var out strings.Builder

	for index, block := range blocks {
		if index > 0 {
			out.WriteString("\n\n")
		}
		for _, span := range block.Spans {
			out.WriteString(spanToHTML(span))
		}
	}
	return out.String()
}

func spanToHTML(span notifymarkup.Span) string {
	text := escapeHTML(span.Text)

	switch {
	case span.Link != "":
		return `<a href="` + escapeHTML(span.Link) + `">` + text + `</a>`
	case span.Code:
		return "<code>" + text + "</code>"
	case span.Bold:
		text = "<b>" + text + "</b>"
	}
	if span.Italic {
		text = "<i>" + text + "</i>"
	}
	if span.Underline {
		text = "<u>" + text + "</u>"
	}
	if span.Strike {
		text = "<s>" + text + "</s>"
	}
	return text
}

// trim keeps the message inside Telegram's limit with its end marked rather than
// refusing it: a notification that does not arrive is worse than a short one.
func trim(text string) string {
	if len(text) <= maxMessage {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxMessage-64]) + "\n…"
}

// titleOf is the title to show, and where it came from.
func titleOf(note notification) string {
	if title, ok := note.Data["title"].(string); ok && strings.TrimSpace(title) != "" {
		return title
	}
	return titleFromFacts(note)
}

// titleFromFacts composes a headline from what the core knows.
//
// Every field is optional and a missing one drops out rather than printing an empty
// label: "Deploy failed" is a better headline than "Job deploy failed" when there is
// no job, and "Failed" is better than nothing at all.
func titleFromFacts(note notification) string {
	level := ""
	if len(note.Levels) > 0 {
		level = note.Levels[0]
	}

	subject, detail := "", ""
	switch {
	case note.Kind == "pipeline.started":
		subject, detail, level = "Pipeline", branchOf(note), "running"
	case note.Kind == "pipeline.finished":
		subject, detail = "Pipeline", branchOf(note)
	case note.Kind == "job.finished":
		subject, detail = "Job", nameOf(note, "job", "name")
	case note.Kind == "job.retried":
		subject, detail, level = "Job", nameOf(note, "job", "name"), "retried"
	}

	// A test has no history to compose from, and says so in its own words.
	if subject == "" {
		if note.Kind == "test" {
			return "Test message"
		}
		return ""
	}

	title := subject
	if detail != "" {
		title += " " + detail
	}
	if level != "" {
		title += " " + level
	}
	return title
}

func branchOf(note notification) string {
	if branch, ok := note.Data["pipeline"].(map[string]any); ok {
		if ref, ok := branch["ref"].(string); ok && ref != "" {
			return ref
		}
	}
	return ""
}

func nameOf(note notification, group, key string) string {
	if section, ok := note.Data[group].(map[string]any); ok {
		if name, ok := section[key].(string); ok {
			return name
		}
	}
	return ""
}

// escapeHTML keeps a branch name or a commit message from breaking the message.
// escapeHTML is applied to text this module did not write the markup for.
//
// Every message goes through it exactly once, inside spanToHTML: a branch name
// containing an ampersand must not become an HTML entity the reader sees, and Telegram
// rejects a message whose tags do not balance.
func escapeHTML(text string) string {
	return htmlEscaper.Replace(text)
}

var htmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
)

// absoluteURL makes a link from the instance, because whoever reads the message is
// not on this network and a relative link opens nothing.
//
// The separator between the host and the path is the part that is easy to lose: a
// path arrives as "/p/group/project/-/pipelines/12", and trimming the slash off it
// before gluing it to a host produces "localhostp/group/..." — a link that is
// silently wrong in a way nobody notices until somebody clicks it.
func absoluteURL(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}

	// A base that already carries a scheme and a port wins over a bare host name,
	// because an installation behind a port is otherwise unlinkable.
	base := strings.TrimRight(envOr("DOGIT_PUBLIC_URL", ""), "/")
	if base == "" {
		host := envOr("DOGIT_PUBLIC_HOST", "localhost")
		if strings.Contains(host, "://") {
			base = strings.TrimRight(host, "/")
		} else {
			base = "https://" + host
		}
	}
	return base + "/" + strings.TrimLeft(path, "/")
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
func (c *coreClient) notifications(ctx context.Context, after int64) (notificationAnswer, error) {
	var answer notificationAnswer
	if err := c.post(ctx, "/api/v1/module/notifications",
		map[string]any{"after": after}, &answer); err != nil {
		return answer, err
	}
	return answer, nil
}

// resumeFrom asks where this module was last time it looked, so a restart carries
// on instead of starting again. A core that has never been asked answers zero, which
// is the right answer for a module being installed for the first time.
func (c *coreClient) resumeFrom(ctx context.Context) int64 {
	answer, err := c.notifications(ctx, 0)
	if err != nil {
		return 0
	}
	return answer.ResumeFrom
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
