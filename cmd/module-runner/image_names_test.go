package main

import "testing"

// The names an image is published under, which is a question about what somebody will
// be able to find later.
func TestImageNames(t *testing.T) {
	cases := []struct {
		name  string
		build map[string]any
		env   map[string]string
		want  []string
	}{
		{"a tag and a commit", map[string]any{}, map[string]string{
			"CI_COMMIT_TAG": "v1.01", "CI_COMMIT_SHORT_SHA": "abc1234",
		}, []string{"v1.01", "abc1234"}},
		{"a commit alone", map[string]any{}, map[string]string{
			"CI_COMMIT_SHORT_SHA": "abc1234",
		}, []string{"abc1234"}},
		{"the file asks for the tag and there is one", map[string]any{"tag": "v1.01"},
			map[string]string{"CI_COMMIT_TAG": "v1.01", "CI_COMMIT_SHORT_SHA": "abc1234"},
			[]string{"v1.01", "abc1234"}},
		// The case that used to produce an empty image name.
		{"the file asks for the tag and there is none", map[string]any{"tag": ""},
			map[string]string{"CI_COMMIT_SHORT_SHA": "abc1234"}, []string{"abc1234"}},
		{"a name of the repository's own is kept", map[string]any{"tag": "candidate"},
			map[string]string{"CI_COMMIT_SHORT_SHA": "abc1234"}, []string{"candidate", "abc1234"}},
		{"never latest", map[string]any{}, map[string]string{}, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := imageNamesFor(tc.build, tc.env)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("names = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("names = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
