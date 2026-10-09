// Package notifymarkup is the small amount of formatting a notification may use.
//
// A notification has to read well in a Telegram message, in an email, in a Slack post
// and in a JSON webhook, and those four have four different ways of saying "bold".
// Writing the words in any one of them would mean every other channel gets somebody
// else's punctuation: asterisks in an email, raw HTML in a chat, a link that does not
// survive being pasted anywhere.
//
// So the words are written once, in this, and each channel translates what it can. It
// is deliberately the smallest subset that survives being translated — bold, italic,
// underline, struck-through, code and links, with a blank line between paragraphs.
// Anything a channel cannot show is rendered as plain text rather than shown raw,
// because a message full of asterisks reads as a broken message.
//
// It is not a general-purpose markup language and will not grow into one. A pipeline's
// message is two lines of words about one build; the point is that they read the same
// everywhere, not that they can do everything.
package notifymarkup

import (
	"strings"
)

// Block is one paragraph.
type Block struct {
	Spans []Span
}

// Span is a run of text with some formatting on it.
//
// The marks are flat rather than a tree because that is all a message needs: bold
// inside a link inside a sentence is not a thing anybody writes in a build
// notification, and a tree would only be a thing to get wrong.
type Span struct {
	Text string

	Bold      bool
	Italic    bool
	Underline bool
	Strike    bool
	Code      bool
	// Link is where the text goes when it is clicked, empty when it is not a link.
	Link string
}

// Plain is the text with every mark removed.
//
// What a channel shows when it cannot show formatting at all, and what any subject line
// shows: "Build failed" rather than "**Build** failed".
func Plain(text string) string {
	blocks := Parse(text)

	paragraphs := make([]string, 0, len(blocks))
	for _, block := range blocks {
		var line strings.Builder
		for _, span := range block.Spans {
			line.WriteString(span.Text)
		}
		paragraphs = append(paragraphs, line.String())
	}
	return strings.Join(paragraphs, "\n\n")
}

// Parse reads a message into paragraphs.
//
// A blank line between paragraphs, because a message written on two lines is two lines
// of one sentence as often as it is two sentences — and a channel with no paragraphs
// at all wants them as one line anyway.
func Parse(text string) []Block {
	blocks := []Block{}

	for _, paragraph := range paragraphs(text) {
		spans := parseSpans(paragraph)
		if strings.TrimSpace(plainOf(spans)) == "" {
			continue
		}
		blocks = append(blocks, Block{Spans: spans})
	}
	return blocks
}

func plainOf(spans []Span) string {
	var out strings.Builder
	for _, span := range spans {
		out.WriteString(span.Text)
	}
	return out.String()
}

func paragraphs(text string) []string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")

	found := []string{}
	current := []string{}

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if len(current) > 0 {
				found = append(found, strings.Join(current, "\n"))
				current = nil
			}
			continue
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		found = append(found, strings.Join(current, "\n"))
	}
	return found
}

// mark is one thing written as text and read as formatting.
type mark struct {
	open  string
	close string
	apply func(*Span)
}

// marks are ordered longest first: `***` has to be tried before `**`, or a bold italic
// run is read as a bold run followed by a stray asterisk.
var marks = []mark{
	{"***", "***", func(s *Span) { s.Bold, s.Italic = true, true }},
	{"**", "**", func(s *Span) { s.Bold = true }},
	{"__", "__", func(s *Span) { s.Underline = true }},
	{"~~", "~~", func(s *Span) { s.Strike = true }},
	{"`", "`", func(s *Span) { s.Code = true }},
	{"_", "_", func(s *Span) { s.Italic = true }},
}

// parseSpans reads one paragraph.
//
// A mark that is never closed is text, not formatting. That is the difference between
// `**unclosed` and `**bold**`, and it is the difference between a message about a
// commit and a message with an underscore eaten out of a file path: branch names,
// paths and version numbers all contain marks, and a parser that swallows them sends
// a notification about the wrong thing without looking wrong.
func parseSpans(text string) []Span {
	// Offsets that are to be read as plain text. Grown one at a time: a mark that
	// turned out to have no partner gives up its place in the text, and the paragraph
	// is read again with that one mark treated as a character.
	literal := map[int]bool{}

	for range marks {
		spans, unclosed := parseAttempt(text, literal)
		if unclosed < 0 {
			return spans
		}
		literal[unclosed] = true
	}

	// Every mark in the message gave up. What is left is text, and this is the
	// message somebody actually typed.
	spans, _ := parseAttempt(text, literal)
	return spans
}

// parseAttempt reads the paragraph once. It returns the offset of a mark that was
// opened and never closed, or -1 when nothing was left open.
func parseAttempt(text string, literal map[int]bool) ([]Span, int) {
	spans := []Span{}
	open := []openMark{}
	word := strings.Builder{}

	emit := func() {
		if word.Len() == 0 {
			return
		}
		span := Span{Text: word.String()}
		for _, one := range open {
			marks[one.which].apply(&span)
		}
		word.Reset()
		spans = append(spans, span)
	}

	for i := 0; i < len(text); {
		if literal[i] {
			word.WriteByte(text[i])
			i++
			continue
		}

		if text[i] == '[' {
			if span, width, ok := parseLink(text[i:]); ok {
				emit()
				spans = append(spans, span)
				i += width
				continue
			}
		}

		if which, width, ok := matchMark(text, i, open, literal); ok {
			emit()
			switch which {
			case closeMark:
				open = open[:len(open)-1]
			default:
				open = append(open, openMark{which: which, at: i})
			}
			i += width
			continue
		}

		word.WriteByte(text[i])
		i++
	}

	emit()

	// The first mark still open is the one that gives up its place. Marking it as text
	// rather than failing the whole paragraph keeps the rest of the message readable.
	if len(open) > 0 {
		return spans, open[0].at
	}
	return spans, -1
}

// closeMark says which mark matched: one being shut rather than one being opened.
const closeMark = -1

// openMark is a mark that has been read and not yet closed, and where it was written.
//
// The position is what a mark that never closes has to give up: the paragraph is read
// again with that one character treated as text.
type openMark struct {
	which int
	at    int
}

// matchMark reads one formatting mark at this position.
//
// It only closes what is actually open: `a * b` is three words and a star. A parser
// that pairs markers by counting ends up italicising whatever happens to lie between
// the first star and the second one in the message.
func matchMark(text string, at int, open []openMark, literal map[int]bool) (int, int, bool) {
	for index, candidate := range marks {
		if !strings.HasPrefix(text[at:], candidate.open) {
			continue
		}

		// The innermost open mark of this kind is the one being closed.
		for depth := len(open) - 1; depth >= 0; depth-- {
			if marks[open[depth].which].close != candidate.close {
				continue
			}
			// Everything opened inside it closes with it, so `**a _b_ c**` ends with the
			// italic shut rather than the italic running on to the end.
			return closeMark, len(candidate.close), true
		}

		return index, len(candidate.open), true
	}
	return 0, 0, false
}

// parseLink reads `[what it says](where it goes)`.
func parseLink(text string) (Span, int, bool) {
	label := strings.Index(text, "]")
	if label == -1 || label+1 >= len(text) || text[label+1] != '(' {
		return Span{}, 0, false
	}

	end := strings.Index(text[label:], ")")
	if end == -1 {
		return Span{}, 0, false
	}
	end += label

	text2 := text[1:label]
	href := text[label+2 : end]
	if strings.TrimSpace(text2) == "" || strings.TrimSpace(href) == "" {
		return Span{}, 0, false
	}
	return Span{Text: text2, Link: href}, end + 1, true
}
