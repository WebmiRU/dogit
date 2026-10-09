package k8s

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// A place may be pointed at another registry, and the image it rolls out must not change
// because of it: the path, the tag and the digest are the image, and only the host says
// where it is fetched from. An implementation that rebuilt the name would drop the digest —
// which is the one thing that makes the rollout the thing that was built.
func TestAnImageMovesToAnotherRegistryAndKeepsEverythingElse(t *testing.T) {
	cases := []struct {
		name, image, address, want string
	}{
		{
			name:    "a mirror of the same storage",
			image:   "192.168.1.103:8091/test/versions@sha256:2b3c4d5e",
			address: "mirror.example.com",
			want:    "mirror.example.com/test/versions@sha256:2b3c4d5e",
		},
		{
			name:    "a mirror with a port and a path",
			image:   "registry.example.com:5000/group/app:v1.2",
			address: "harbor.example.com/v2",
			want:    "harbor.example.com/v2/group/app:v1.2",
		},
		{
			name:    "the same address written with a slash is the same address",
			image:   "192.168.1.103:8091/test/versions@sha256:2b3c4d5e",
			address: "192.168.1.103:8091/",
			want:    "192.168.1.103:8091/test/versions@sha256:2b3c4d5e",
		},
		{
			name:    "an unqualified name has no host to replace",
			image:   "nginx:1.25",
			address: "registry.example.com",
			want:    "registry.example.com/nginx:1.25",
		},
		{
			name:    "no address means the image is left alone",
			image:   "192.168.1.103:8091/test/versions@sha256:2b3c4d5e",
			address: "",
			want:    "192.168.1.103:8091/test/versions@sha256:2b3c4d5e",
		},
		{
			name:    "no image means nothing to move",
			image:   "",
			address: "mirror.example.com",
			want:    "",
		},
		{
			// A person writes a scheme and a slash; an image name carries neither. Left
			// in, the address would not match the one the image already has and the
			// workload would be pointed at a host nothing serves.
			name:    "an address written the way a person writes it",
			image:   "192.168.1.103:8091/test/versions@sha256:2b3c4d5e",
			address: "https://192.168.1.103:8091/",
			want:    "192.168.1.103:8091/test/versions@sha256:2b3c4d5e",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ImageAtRegistry(c.image, c.address); got != c.want {
				t.Errorf("ImageAtRegistry(%q, %q) = %q, want %q", c.image, c.address, got, c.want)
			}
		})
	}
}

// A registry that issues neither tokens nor passwords is reached with its own login, and
// the secret the cluster reads has to carry it — otherwise the pod sits at
// ImagePullBackOff with a credential that was written and never looked at.
//
// Read the way the kubelet reads it: the one `auth` entry under the address, base64 of
// "user:password". Looking for the login as plain text in the document would be looking for
// something a docker config never contains.
func TestThePullSecretCarriesALoginWhenThereIsOne(t *testing.T) {
	document := dockerConfigJSON(PullSecret{
		Address:  "harbor.example.com",
		Username: "robot$ci",
		Token:    "s3cret",
	})

	entry, ok := authEntry(t, document, "harbor.example.com")
	if !ok {
		return
	}
	if entry != "robot$ci:s3cret" {
		t.Errorf("the kubelet would read %q, want the login and the password", entry)
	}
}

// And a token still arrives as a token: an empty username with a token is what an anonymous
// bearer pull looks like, and a registry that issues tokens does care about the difference.
func TestATokenStillArrivesAsAToken(t *testing.T) {
	document := dockerConfigJSON(PullSecret{
		Address: "192.168.1.103:8091",
		Token:   "glpat-token",
	})

	entry, ok := authEntry(t, document, "192.168.1.103:8091")
	if !ok {
		return
	}
	if entry != "<token>:glpat-token" {
		t.Errorf("the kubelet would read %q, want a token in the password field", entry)
	}
}

// authEntry is what the cluster would read for one registry: the credentials, decoded.
func authEntry(t *testing.T, document []byte, address string) (string, bool) {
	t.Helper()

	var parsed struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(document, &parsed); err != nil {
		t.Fatalf("the secret is not a docker config: %v (%s)", err, document)
	}
	raw, ok := parsed.Auths[address]
	if !ok {
		t.Fatalf("the secret has no entry for %s: %s", address, document)
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(raw.Auth)
	if err != nil {
		t.Fatalf("the entry for %s is not base64: %v", address, err)
		return "", false
	}
	return string(decoded), true
}
