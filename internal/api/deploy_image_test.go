package api

import "testing"

// Which image a deployment rolls out, and where it pulls from.
//
// Both of these were wrong in the same way, and both cost a rollout: the image was
// empty, so nothing was substituted, and the registry address carried a path, so the
// credential was filed under a key no client looks up.

// An image name is a repository and a tag, and the tag is often not in it.
func TestAnImageIsSplitIntoItsRepositoryAndTag(t *testing.T) {
	cases := []struct {
		image      string
		repository string
		tag        string
	}{
		{"127.0.0.1:8091/home-store/www", "127.0.0.1:8091/home-store/www", ""},
		{"127.0.0.1:8091/home-store/www:dev", "127.0.0.1:8091/home-store/www", "dev"},
		{"registry.example.com:5000/group/project:1.2.3", "registry.example.com:5000/group/project", "1.2.3"},
		{"registry:5000/team/app@sha256:abc", "registry:5000/team/app", ""},
	}

	for _, one := range cases {
		repository, tag := splitImage(one.image)
		if repository != one.repository || tag != one.tag {
			t.Errorf("%q was split into %q/%q, want %q/%q",
				one.image, repository, tag, one.repository, one.tag)
		}
	}
}

// The credential's address is the host and port, never the path.
//
// A colon before the last slash is a port and one after it is a tag, which is the
// whole difference between a registry on port 5000 and an image called "app:dev".
func TestTheRegistryAddressIsTheHostAlone(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8091/home-store/www":     "127.0.0.1:8091",
		"registry.example.com:5000/a/b":     "registry.example.com:5000",
		"registry.example.com/team/app":     "registry.example.com",
		"127.0.0.1:8091/home-store/www:dev": "127.0.0.1:8091",
	}

	for image, want := range cases {
		if got := registryHostFrom(image); got != want {
			t.Errorf("%q gave the address %q, want %q — a credential filed under a path is one no client looks up",
				image, got, want)
		}
	}
}
