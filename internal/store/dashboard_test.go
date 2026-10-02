package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ewolf/dogit/internal/models"
)

func mustPayload(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDescribeEventPush(t *testing.T) {
	tests := []struct {
		name        string
		refUpdates  any
		wantSummary string
		wantDetail  string
	}{
		{
			name: "single branch",
			refUpdates: map[string]any{"ref_updates": []map[string]any{
				{"ref": "refs/heads/main", "new_sha": "abc", "shas": []string{"abc"}},
			}},
			wantSummary: "pushed to main",
			wantDetail:  "1 commit",
		},
		{
			name: "two branches",
			refUpdates: map[string]any{"ref_updates": []map[string]any{
				{"ref": "refs/heads/main", "new_sha": "abc", "shas": []string{"abc", "def"}},
				{"ref": "refs/heads/topic", "new_sha": "def", "shas": []string{}},
			}},
			wantSummary: "pushed to main, topic",
			wantDetail:  "2 commits",
		},
		{
			name: "deletion has no commit count",
			refUpdates: map[string]any{"ref_updates": []map[string]any{
				{"ref": "refs/heads/old", "new_sha": "0000000000000000000000000000000000000000", "shas": []string{}},
			}},
			wantSummary: "deleted old",
		},
		{
			name: "tag only",
			refUpdates: map[string]any{"ref_updates": []map[string]any{
				{"ref": "refs/tags/v1.0", "new_sha": "abc", "shas": []string{"abc"}},
			}},
			wantSummary: "pushed changes",
			wantDetail:  "1 commit",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry := ActivityEntry{
				Kind:      models.EventPush,
				ActorName: "alice",
			}
			describeEvent(&entry, mustPayload(t, tc.refUpdates))

			if entry.Summary != tc.wantSummary {
				t.Errorf("summary = %q, want %q", entry.Summary, tc.wantSummary)
			}
			if tc.wantDetail != "" && entry.Detail != tc.wantDetail {
				t.Errorf("detail = %q, want %q", entry.Detail, tc.wantDetail)
			}
		})
	}
}

// The payload is written by the git hook, so a malformed or unexpected one must
// degrade gracefully instead of breaking the whole feed.
func TestDescribeEventToleratesBadPayload(t *testing.T) {
	for _, payload := range [][]byte{
		nil,
		[]byte("not json"),
		[]byte(`{"ref_updates":"wrong type"}`),
		[]byte(`{}`),
	} {
		entry := ActivityEntry{Kind: models.EventPush}
		describeEvent(&entry, payload)

		if entry.Summary == "" {
			t.Errorf("payload %q produced an empty summary", payload)
		}
		if entry.ActorName == "" {
			t.Errorf("payload %q produced an empty actor", payload)
		}
	}
}

func TestDescribeEventFillsMissingNames(t *testing.T) {
	entry := ActivityEntry{Kind: models.EventJobUpdated, CreatedAt: time.Now()}
	describeEvent(&entry, nil)

	if entry.ActorName != "someone" {
		t.Errorf("actor = %q, want a fallback", entry.ActorName)
	}
	if entry.Summary == "" {
		t.Error("expected a generic summary for an unhandled event kind")
	}
}

func TestPlural(t *testing.T) {
	cases := map[int]string{0: "0 commits", 1: "1 commit", 2: "2 commits", 42: "42 commits"}
	for count, want := range cases {
		if got := plural(count, "commit", "commits"); got != want {
			t.Errorf("plural(%d) = %q, want %q", count, got, want)
		}
	}
}

func TestAppendUnique(t *testing.T) {
	list := []string{"main"}
	list = appendUnique(list, "main")
	list = appendUnique(list, "topic")
	list = appendUnique(list, "")

	if len(list) != 2 || list[0] != "main" || list[1] != "topic" {
		t.Errorf("got %v, want [main topic]", list)
	}
}
