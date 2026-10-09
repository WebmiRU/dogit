package store

import (
	"encoding/json"
	"strings"

	"github.com/ewolf/dogit/internal/models"
)

// describeEvent fills the human-readable fields of an activity entry from the
// event payload.
//
// The payload is written by the git hook, so it is treated as untrusted input:
// every field is length-limited and unknown kinds fall back to a generic summary
// rather than failing the whole feed.
func describeEvent(e *ActivityEntry, payload []byte) {
	if e.ProjectName == "" {
		e.ProjectName = e.ProjectPath
	}
	if e.ActorName == "" {
		e.ActorName = "someone"
	}

	var body struct {
		RefUpdates []struct {
			Ref    string   `json:"ref"`
			Branch string   `json:"branch"`
			NewSHA string   `json:"new_sha"`
			Shas   []string `json:"shas"`
		} `json:"ref_updates"`
	}
	_ = json.Unmarshal(payload, &body) // a malformed payload just yields fewer details

	switch e.Kind {
	case models.EventPush:
		branches := []string{}
		deleted := []string{}
		commits := 0

		for _, update := range body.RefUpdates {
			commits += len(update.Shas)
			switch {
			case strings.EqualFold(update.NewSHA, "") || strings.Trim(update.NewSHA, "0") == "":
				deleted = append(deleted, shortRef(update.Ref))
			case strings.HasPrefix(update.Ref, "refs/tags/"):
				// Tags are not branches; they are mentioned separately below.
			default:
				branches = appendUnique(branches, shortRef(update.Ref))
			}
		}

		switch {
		case len(deleted) > 0:
			e.Summary = "deleted " + strings.Join(deleted, ", ")
		case len(branches) > 0:
			e.Summary = "pushed to " + strings.Join(branches, ", ")
		default:
			e.Summary = "pushed changes"
		}
		if commits > 0 {
			e.Detail = plural(commits, "commit", "commits")
		}

	case models.EventPipelineCreated:
		e.Summary = "started a pipeline"

	case models.EventPipelineUpdated:
		e.Summary = "updated a pipeline"

	case models.EventJobUpdated:
		e.Summary = "updated a job"

	case models.EventMergeRequestChanged:
		e.Summary = "updated a merge request"

	default:
		e.Summary = "made a change"
	}
}

func shortRef(ref string) string {
	ref = strings.TrimPrefix(ref, "refs/heads/")
	ref = strings.TrimPrefix(ref, "refs/tags/")
	return ref
}

func appendUnique(list []string, value string) []string {
	if value == "" {
		return list
	}
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

func plural(count int, one, many string) string {
	if count == 1 {
		return "1 " + one
	}
	return itoaSmall(count) + " " + many
}

func itoaSmall(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
