package notifymarkup

import (
	"strings"
	"testing"
)

// The words of a notification are written once, and each channel reads them its own
// way. So the tests here are about what a channel is asked to show, and about the one
// thing this must never do: guess. A message full of asterisks reads as a broken
// message, and a parser that pairs markers by counting will happily italicise a commit
// message that happens to contain a star.

func only(t *testing.T, text string) Span {
	t.Helper()

	blocks := Parse(text)
	if len(blocks) != 1 || len(blocks[0].Spans) != 1 {
		t.Fatalf("%q was read as %d paragraphs: %+v", text, len(blocks), blocks)
	}
	return blocks[0].Spans[0]
}

func TestTheMarksAreRead(t *testing.T) {
	for _, one := range []struct {
		text  string
		check func(Span) bool
		what  string
	}{
		{"**bold**", func(s Span) bool { return s.Bold && s.Text == "bold" }, "bold"},
		{"_italic_", func(s Span) bool { return s.Italic && s.Text == "italic" }, "italic"},
		{"__underline__", func(s Span) bool { return s.Underline && s.Text == "underline" }, "underline"},
		{"~~struck~~", func(s Span) bool { return s.Strike && s.Text == "struck" }, "struck"},
		{"`code`", func(s Span) bool { return s.Code && s.Text == "code" }, "code"},
		{"***both***", func(s Span) bool { return s.Bold && s.Italic && s.Text == "both" }, "bold italic"},
	} {
		span := only(t, one.text)
		if !one.check(span) {
			t.Errorf("%q was read as %+v", one.text, span)
		}
		if span.Link != "" {
			t.Errorf("%q was read as a link to %q", one.text, span.Link)
		}
	}
}

// A link carries both halves: what it says and where it goes.
func TestALinkKeepsItsTextAndItsAddress(t *testing.T) {
	span := only(t, "[open the run](/p/www/-/pipelines/22)")

	if span.Text != "open the run" {
		t.Errorf("the link says %q", span.Text)
	}
	if span.Link != "/p/www/-/pipelines/22" {
		t.Errorf("the link goes to %q", span.Link)
	}
}

// A word with an odd number of stars is a word with a star in it. This is the case
// that matters in practice: commit messages contain asterisks.
func TestAnUnmatchedMarkIsShownAsItWasWritten(t *testing.T) {
	if got := Plain("fix * in the parser"); got != "fix * in the parser" {
		t.Errorf("a lone star was swallowed: %q", got)
	}
	if got := Plain("2 * 3 is not 6"); got != "2 * 3 is not 6" {
		t.Errorf("arithmetic was read as formatting: %q", got)
	}
	if got := Plain("**unclosed"); got != "**unclosed" {
		t.Errorf("an unclosed mark was swallowed: %q", got)
	}
}

// Italic is the one that goes wrong most easily: an underscore appears in branch
// names, file paths and version numbers.
func TestAnUnderscoreInAPathIsNotEmphasis(t *testing.T) {
	got := Plain("built from feature/add_login in #22")

	if got != "built from feature/add_login in #22" {
		t.Errorf("a path was read as formatting: %q", got)
	}
	if span := only(t, "_emphasis_"); !span.Italic {
		t.Errorf("a deliberately marked word was not emphasis: %+v", span)
	}
}

// A blank line is a paragraph. A single newline is not: it is two lines of one
// sentence as often as it is two sentences.
func TestBlankLinesSeparateParagraphs(t *testing.T) {
	blocks := Parse("first line\nstill the first\n\nsecond paragraph")

	if len(blocks) != 2 {
		t.Fatalf("the text was read as %d paragraphs", len(blocks))
	}
	if got := Plain("first line\nstill the first\n\nsecond paragraph"); got != "first line\nstill the first\n\nsecond paragraph" {
		t.Errorf("Plain changed the message: %q", got)
	}
}

// Trailing blank lines are how people end a message, not an empty paragraph.
func TestTrailingBlankLinesAreNotAParagraph(t *testing.T) {
	if blocks := Parse("something happened\n\n\n"); len(blocks) != 1 {
		t.Errorf("trailing blank lines became %d paragraphs", len(blocks))
	}
}

// Formatting nests one level, which is all a message about a build needs.
func TestMarksNestOneLevel(t *testing.T) {
	blocks := Parse("**bold with _italic_ inside**")
	if len(blocks) != 1 {
		t.Fatalf("read as %d paragraphs", len(blocks))
	}

	var sawBold, sawItalic bool
	for _, span := range blocks[0].Spans {
		if span.Bold && span.Text == "bold with " {
			sawBold = true
		}
		if span.Italic && span.Text == "italic" {
			sawItalic = true
		}
	}
	if !sawBold || !sawItalic {
		t.Errorf("nesting was read as %+v", blocks[0].Spans)
	}
}

// Nothing here should ever panic on input written by a person at midnight.
func TestNothingPanicsOnOddInput(t *testing.T) {
	for _, text := range []string{
		"", "**", "***", "____", "``", "[]", "[", "[]()", "[a](", "a](b)", "[a]b",
		"~~`__***", "\n\n\n", "   ", "[a](b)[c](d)", "**a[b](c)**",
	} {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Errorf("parsing %q panicked: %v", text, recovered)
				}
			}()
			_ = Parse(text)
			_ = Plain(text)
		}()
	}
}

// The renderer a channel uses: what it can show it shows, and what it cannot it shows
// as plain text rather than as marks.
func TestAChannelCanRefuseAMarkAndStillSendTheWords(t *testing.T) {
	blocks := Parse("**bold** and ~~struck~~")
	if len(blocks) != 1 {
		t.Fatalf("read as %d paragraphs", len(blocks))
	}

	// A channel that supports neither writes what it read.
	var out strings.Builder
	for _, span := range blocks[0].Spans {
		out.WriteString(span.Text)
	}
	if out.String() != "bold and struck" {
		t.Errorf("a channel that supports no marks wrote %q", out.String())
	}
}
