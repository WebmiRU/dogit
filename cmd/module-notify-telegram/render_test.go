package main

import (
	"strings"
	"testing"
)

// What a message looks like in Telegram.
//
// The words are written once, in the shared markup, and this module translates them.
// The tests are about the translation rather than about Telegram: every mark is opened
// and closed inside its own span, because Telegram rejects a message whose tags do not
// balance — and a rejected message is one nobody sees, which is worse than a plain one.

func note(text string, level string, data map[string]any) notification {
	return notification{
		ID: 1, Kind: "job.finished", Text: text, Levels: []string{level}, Data: data,
	}
}

func TestTheTitleIsBoldAndSeparatedByABlankLine(t *testing.T) {
	got := render(note("the body", "failure", map[string]any{"title": "Build failed"}))

	want := "<b>Build failed</b>\n\nthe body"
	if got != want {
		t.Errorf("the message is\n%q\nwant\n%q", got, want)
	}
}

// Without a title from the pipeline, the headline is composed from the facts — but
// only from facts that are there.
func TestATitleIsComposedWhenThePipelineWroteNone(t *testing.T) {
	got := render(note("the body", "failure", map[string]any{
		"job":      map[string]any{"name": "deploy"},
		"pipeline": map[string]any{"ref": "main"},
	}))

	if !strings.HasPrefix(got, "<b>Job deploy failure</b>") {
		t.Errorf("the composed headline is missing: %q", got)
	}
}

// A test has no history to compose from and says so in its own words.
func TestATestMessageHasItsOwnHeadline(t *testing.T) {
	got := render(notification{ID: 1, Kind: "test", Text: "one message"})

	if got != "<b>Test message</b>\n\none message" {
		t.Errorf("a test message reads %q", got)
	}
}

// The markup is translated, and nothing is passed through unescaped.
func TestTheMarkupBecomesTelegramHTML(t *testing.T) {
	// Only the body is compared: a headline is composed from the facts above it, and
	// these cases are about the marks in the text.
	for _, one := range []struct{ in, want string }{
		{"**bold**", "<b>bold</b>"},
		{"_italic_", "<i>italic</i>"},
		{"__under__", "<u>under</u>"},
		{"~~struck~~", "<s>struck</s>"},
		{"`code`", "<code>code</code>"},
		{"[the run](/p/www/-/pipelines/9)", `<a href="/p/www/-/pipelines/9">the run</a>`},
		// A branch name that looks like markup is text, not emphasis.
		{"feature/add_login", "feature/add_login"},
	} {
		body := render(note(one.in, "success", nil))
		if _, after, found := strings.Cut(body, "\n\n"); found {
			body = after
		}
		if body != one.want {
			t.Errorf("%q became %q, want %q", one.in, body, one.want)
		}
	}
}

// A message that arrives broken is a message nobody sees. Tags are balanced, and text
// is escaped exactly once.
func TestNothingInAMessageCanBreakIt(t *testing.T) {
	got := render(note(`a < b & c > d`, "success", map[string]any{"title": `5 < 6`}))

	if strings.Contains(got, "< b") {
		t.Errorf("raw angle brackets reached Telegram: %q", got)
	}
	if strings.Count(got, "<b>") != strings.Count(got, "</b>") {
		t.Errorf("the bold tags do not balance: %q", got)
	}
}

// A long message is shortened rather than refused, and says that it was.
func TestALongMessageIsTrimmedRatherThanRefused(t *testing.T) {
	got := render(note(strings.Repeat("x", maxMessage*2), "success", nil))

	if len([]rune(got)) > maxMessage {
		t.Errorf("the message is %d characters, over Telegram's limit", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a trimmed message does not say it was trimmed: %q", got[len(got)-40:])
	}
}
