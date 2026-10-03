package api

import (
	"fmt"
	"strconv"
	"strings"
)

// What a notification's text may say about itself.
//
// A fixed dictionary, on purpose. A template language with expressions in it would be
// something to read and to get wrong, and the things a notification needs to mention
// — which project, which run, which job, which image — are known before anybody writes
// a word of the message.
//
// An unknown name is refused rather than filled with nothing. A message that says "
// ${job.name} failed" because the key was misspelled is worse than no message: it
// looks delivered, and it says nothing.

// substitutionValues is everything a message may refer to, for one event.
func substitutionValues(context notifyContext) map[string]string {
	out := map[string]string{}

	// Project.
	for _, key := range []string{"path", "name", "default_branch"} {
		out["project."+key] = textOf(context.Project[key])
	}

	// Run.
	for _, key := range []string{"iid", "ref", "sha", "sha_short", "url",
		"commit_title", "commit_author", "status"} {
		out["pipeline."+key] = textOf(context.Pipeline[key])
	}
	if iid, ok := context.Pipeline["iid"]; ok {
		out["pipeline.iid"] = textOf(iid)
	}

	// Job.
	if context.Job != nil {
		for _, key := range []string{"name", "stage", "status"} {
			out["job."+key] = textOf(context.Job[key])
		}
		if ms, ok := context.Job["duration_ms"]; ok {
			out["job.duration"] = durationOf(ms)
		}
	}

	// Images.
	//
	// Names and tags joined when a job built more than one, rather than the first of
	// them: a template that says ${image.name} should say all of what was built, and
	// picking one silently would be a message about something else.
	names, tags := []string{}, []string{}
	for _, image := range context.Images {
		if name := textOf(image["name"]); name != "" {
			names = append(names, name)
		}
		if tag := textOf(image["tag"]); tag != "" {
			tags = append(tags, tag)
		}
	}
	out["image.name"] = strings.Join(names, ", ")
	out["image.tag"] = strings.Join(tags, ", ")

	return out
}

// substitute fills a template from what is known about the event.
//
// It reports every name it could not fill, and a name that exists but has nothing to
// say counts as one of those. A template asking a job's name of a run that has no job
// would otherwise produce "home-store/www #21 — :", which reads as delivered and
// tells nobody anything — the worst shape a notification can have.
//
// The caller does not send the message in that case, and says why in the log where
// whoever wrote the template will look.
func substitute(template string, values map[string]string) (string, []string) {
	var missing []string

	var out strings.Builder
	for {
		start := strings.Index(template, "${")
		if start == -1 {
			break
		}
		end := strings.Index(template[start:], "}")
		if end == -1 {
			// An unclosed ${ is not a name at all. Reported the same way, because the
			// result is the same: a message that cannot be sent.
			out.WriteString(template)
			return out.String(), append(missing, template[start:])
		}
		end += start

		out.WriteString(template[:start])
		name := template[start+2 : end]
		value := values[name]
		if strings.TrimSpace(value) == "" {
			missing = append(missing, name)
			out.WriteString(template[start : end+1])
		} else {
			out.WriteString(value)
		}
		template = template[end+1:]
	}
	out.WriteString(template)

	return out.String(), missing
}

// renderNotification fills in what the pipeline's own words asked for.
//
// An entry with no text falls back to what the core would have said anyway: the
// repository's configuration is allowed to decide whether a run speaks and what it
// calls itself, and it is not required to rewrite the wording to be allowed to speak.
func renderNotification(entry pipelineNotifyEntry, context notifyContext, fallback string) (title, text string, missing []string) {
	title = entry.Title
	if title != "" {
		title, missing = substitute(title, substitutionValues(context))
	}

	text = entry.Text
	if text != "" {
		text, missing = substitute(text, substitutionValues(context))
	} else {
		text = fallback
	}
	return title, text, missing
}

// durationOf is how long a job took, in the words a person uses.
func durationOf(value any) string {
	ms, ok := value.(int64)
	if !ok {
		if number, isNumber := value.(float64); isNumber {
			ms = int64(number)
		} else {
			return textOf(value)
		}
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	seconds := float64(ms) / 1000
	if seconds < 60 {
		return fmt.Sprintf("%.1fs", seconds)
	}
	return fmt.Sprintf("%dm %ds", int(seconds)/60, int(seconds)%60)
}

func textOf(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprint(typed)
	}
}
