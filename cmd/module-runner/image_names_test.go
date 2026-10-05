package main

import "testing"

// The names an image is published under, which is a question about what somebody will
// be able to find later.
func TestImageNames(t *testing.T) {
	cases := []struct {
		name  string
		build map[string]any
		env   map[string]string
		tags  []string
		want  []string
	}{
		{"a tag and a commit", map[string]any{}, map[string]string{
			"CI_COMMIT_TAG": "v1.01", "CI_COMMIT_SHORT_SHA": "abc1234",
		}, nil, []string{"v1.01", "abc1234"}},
		{"a commit alone", map[string]any{}, map[string]string{
			"CI_COMMIT_SHORT_SHA": "abc1234",
		}, nil, []string{"abc1234"}},
		{"the file asks for the tag and there is one", map[string]any{"tag": "v1.01"},
			map[string]string{"CI_COMMIT_TAG": "v1.01", "CI_COMMIT_SHORT_SHA": "abc1234"},
			nil, []string{"v1.01", "abc1234"}},
		// The case that used to produce an empty image name.
		{"the file asks for the tag and there is none", map[string]any{"tag": ""},
			map[string]string{"CI_COMMIT_SHORT_SHA": "abc1234"}, nil, []string{"abc1234"}},
		{"a name of the repository's own is kept", map[string]any{"tag": "candidate"},
			map[string]string{"CI_COMMIT_SHORT_SHA": "abc1234"}, nil,
			[]string{"candidate", "abc1234"}},
		{"never latest", map[string]any{}, map[string]string{}, nil, nil},

		// What a release actually looks like: a commit carrying several tags, reached
		// by a branch rather than by a tag push. Every one of them is a name somebody
		// will look the release up by.
		{"every tag on the commit, then the commit",
			map[string]any{}, map[string]string{"CI_COMMIT_SHORT_SHA": "abc1234"},
			[]string{"v1.01", "v2.00"},
			[]string{"v1.01", "v2.00", "abc1234"}},

		// The tag this run was started by, and the tags the commit carries, are the
		// same name said twice in some repositories; the list must not repeat itself,
		// or an image is pushed twice under one name and the log says so.
		{"a tag on the commit and the run that is that tag",
			map[string]any{}, map[string]string{
				"CI_COMMIT_TAG": "v1.01", "CI_COMMIT_SHORT_SHA": "abc1234",
			}, []string{"v1.01"},
			[]string{"v1.01", "abc1234"}},

		// The repository's own name comes first, because a file that named the image
		// expects to find it there — and the rest follow it rather than pushing it out.
		{"the repository's own name leads, then the tags",
			map[string]any{"tag": "candidate"}, map[string]string{"CI_COMMIT_SHORT_SHA": "abc1234"},
			[]string{"v1.01"},
			[]string{"candidate", "v1.01", "abc1234"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := imageNamesFor(tc.build, tc.env, tc.tags)
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