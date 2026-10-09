package pipeline

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// What a pipeline says about notifications.
//
// This is the only place the decision to speak is made. A settings page can say
// where a message would go; it cannot say whether this particular run should say
// anything, because a project has a hundred pipelines and at most one of them is the
// deployment somebody wants to hear about at three in the morning.
//
// So the shape is small and levels-only. There is no expression language here on
// purpose: a condition that has to be read as code is a condition that will be wrong
// in a way nobody notices until the night it mattered.
//
//	notify:
//	  - on: [failure]
//	    title: The deployment failed
//	    text: |
//	      ${project.path}: ${job.name} did not finish
//	      ${pipeline.url}
//
// No block at all means silence — including when a recipient is configured and
// switched on. `notify: false` says the same thing out loud, for a pipeline that
// used to announce itself and no longer should.
type NotifySpec struct {
	// Present is whether the file said anything about notifications at all. Kept
	// apart from "silent" because a file with no block is silent by having said
	// nothing, and one that says nothing about it must not look like a refusal.
	Present bool
	// Silent is `notify: false`.
	Silent bool
	// Entries are the announcements this pipeline makes, in the order written.
	Entries []NotifyEntry
}

// NotifyEntry is one announcement.
type NotifyEntry struct {
	// On is the levels this entry speaks at: success, failure, canceled, or always
	// for whatever happens. Empty means whatever happens — which is this block
	// having said something, and not a default filling in for silence.
	On []string
	// Event narrows this entry to one kind of thing happening.
	//
	// It is here because levels cannot tell two events apart, and they are told apart
	// by everybody reading a message: a failed job and a failed run are different news,
	// and an entry about a job's name says nothing sensible about the run that
	// contains it. This is a name from a fixed list rather than a condition, so it
	// cannot become a language.
	Event string
	// Title and Text are the author's own words. Both may use ${…} substitutions.
	Title string `yaml:"title"`
	Text  string `yaml:"text"`
}

// The levels a condition may name.
//
// Listed rather than free text: `on: [fail]` matching nothing because this build
// spells it `failure` is a silent pipeline, and a silent pipeline is the worst
// outcome available here.
const (
	NotifySuccess  = "success"
	NotifyFailure  = "failure"
	NotifyCanceled = "canceled"
	NotifyAlways   = "always"
)

// The events a condition may name. The same words the notification queue uses, so
// there is one list to learn rather than two.
const (
	EventPipelineStarted  = "pipeline.started"
	EventPipelineFinished = "pipeline.finished"
	EventJobFinished      = "job.finished"
	EventJobRetried       = "job.retried"
)

var notifyEvents = map[string]bool{
	EventPipelineStarted:  true,
	EventPipelineFinished: true,
	EventJobFinished:      true,
	EventJobRetried:       true,
}

// notifyLevels is what a condition may say.
var notifyLevels = map[string]bool{
	NotifySuccess:  true,
	NotifyFailure:  true,
	NotifyCanceled: true,
	NotifyAlways:   true,
}

// SpeaksAt says whether this entry speaks about one event.
//
// Two filters and neither of them is an expression: which kind of thing happened, and
// how it went. A pipeline with a failed job and a failed run in it wants both, at
// different words, and a level alone cannot say that.
func (e NotifyEntry) SpeaksAt(event, level string) bool {
	if e.Event != "" && e.Event != event {
		return false
	}
	return e.Level(level)
}

// Level says whether this entry speaks at a level.
//
// `always` is a level of its own: it means the entry was written to be sent whatever
// happened, which is what a pipeline that only cares that it ran says.
func (e NotifyEntry) Level(level string) bool {
	if len(e.On) == 0 {
		return true
	}
	for _, one := range e.On {
		switch strings.TrimSpace(strings.ToLower(one)) {
		case NotifyAlways:
			return true
		case level:
			return true
		}
	}
	return false
}

// Announces says whether this pipeline says anything at a level, and what it says.
//
// Nothing at all is silence: no file, no block, `notify: false` and a block whose
// conditions are all for other levels all mean the same thing, and all of them mean
// it on purpose.
func (n NotifySpec) Announces(event, level string) (NotifyEntry, bool) {
	if !n.Present || n.Silent {
		return NotifyEntry{}, false
	}
	for _, entry := range n.Entries {
		if entry.SpeaksAt(event, level) {
			return entry, true
		}
	}
	return NotifyEntry{}, false
}

// parseNotify reads the `notify` key of a configuration.
//
// The three spellings are deliberate and all three are natural: a list of entries, a
// single entry written without the dashes, and `false` for a pipeline that used to
// announce itself.
func parseNotify(node *yaml.Node) (NotifySpec, error) {
	spec := NotifySpec{Present: true}

	if node.Kind == yaml.ScalarNode && (node.Value == "false" || node.Value == "no") {
		spec.Silent = true
		return spec, nil
	}
	if node.Tag == "!!null" {
		return spec, nil
	}

	entries := []*yaml.Node{}
	switch node.Kind {
	case yaml.SequenceNode:
		entries = node.Content
	case yaml.MappingNode:
		entries = []*yaml.Node{node}
	default:
		return spec, fmt.Errorf("notify must be a list of entries, a single entry, or false")
	}

	for _, entry := range entries {
		parsed, err := parseNotifyEntry(entry)
		if err != nil {
			return spec, err
		}
		spec.Entries = append(spec.Entries, parsed)
	}
	return spec, nil
}

func parseNotifyEntry(node *yaml.Node) (NotifyEntry, error) {
	entry := NotifyEntry{}

	var decoded struct {
		On    []string `yaml:"on"`
		Event string   `yaml:"event"`
		Title string   `yaml:"title"`
		Text  string   `yaml:"text"`
	}
	if err := node.Decode(&decoded); err != nil {
		return entry, fmt.Errorf("notify entry: %w", err)
	}
	entry.On, entry.Event = decoded.On, strings.TrimSpace(decoded.Event)
	entry.Title, entry.Text = decoded.Title, decoded.Text

	// Anything this build does not understand is refused rather than ignored. A
	// misspelt `titel:` that is silently dropped is a message somebody will look for
	// in a channel that never received it.
	for index := 0; index+1 < len(node.Content); index += 2 {
		key := node.Content[index].Value
		switch key {
		case "on", "event", "title", "text":
		default:
			return entry, fmt.Errorf(
				"notify entry has no field called %q; it knows on, event, title and text", key)
		}
	}

	if entry.Event != "" && !notifyEvents[entry.Event] {
		return entry, fmt.Errorf(
			"notify says event %q, which does not happen; use %s, %s, %s or %s",
			entry.Event, EventPipelineStarted, EventPipelineFinished,
			EventJobFinished, EventJobRetried)
	}

	for _, level := range entry.On {
		if !notifyLevels[strings.TrimSpace(strings.ToLower(level))] {
			return entry, fmt.Errorf(
				"notify says %q, which is not a level; use %s, %s, %s or %s",
				level, NotifySuccess, NotifyFailure, NotifyCanceled, NotifyAlways)
		}
	}

	// An entry with neither a title nor text is not a half-written one: it says
	// "announce this, and let whoever it goes to say it". The recipient's own wording
	// fills the gap, and where it does not, the core's line of facts does.
	return entry, nil
}
