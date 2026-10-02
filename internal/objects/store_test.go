package objects

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// Each store runs the same behavioural suite. The local store is always
// available; the remote one runs only when credentials are configured, so the
// suite stays runnable offline.
func testStores(t *testing.T) map[string]func(*testing.T) Store {
	t.Helper()
	return map[string]func(*testing.T) Store{
		"local": func(t *testing.T) Store {
			local, err := NewLocal(t.TempDir(), "dogit")
			if err != nil {
				t.Fatalf("local store: %v", err)
			}
			return local
		},
		"s3": newRemoteStore,
	}
}

// newRemoteStore builds an S3 store from the environment, skipping the test when
// no endpoint is configured.
func newRemoteStore(t *testing.T) Store {
	t.Helper()

	for _, key := range []string{
		"DOGIT_S3_ENDPOINT", "DOGIT_S3_ACCESS_KEY",
		"DOGIT_S3_SECRET_KEY", "DOGIT_S3_BUCKET",
	} {
		if os.Getenv(key) == "" {
			t.Skipf("%s is not set, skipping the remote store", key)
		}
	}

	remote, err := NewS3(S3Options{
		Endpoint:  os.Getenv("DOGIT_S3_ENDPOINT"),
		AccessKey: os.Getenv("DOGIT_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("DOGIT_S3_SECRET_KEY"),
		Bucket:    os.Getenv("DOGIT_S3_BUCKET"),
		Prefix:    "dogit-test",
	})
	if err != nil {
		t.Fatalf("s3 store: %v", err)
	}
	return remote
}

func TestPutGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	for name, build := range testStores(t) {
		t.Run(name, func(t *testing.T) {
			store := build(t)
			content := []byte("hello from dogit\n")

			put, err := store.Put(ctx, "probe/hello.txt", bytes.NewReader(content), int64(len(content)), "text/plain")
			if err != nil {
				t.Fatalf("put: %v", err)
			}
			if put.Size != int64(len(content)) {
				t.Errorf("size = %d, want %d", put.Size, len(content))
			}

			reader, info, err := store.Get(ctx, "probe/hello.txt")
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			defer reader.Close()

			got, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if !bytes.Equal(got, content) {
				t.Errorf("content = %q, want %q", got, content)
			}
			if info.Size != int64(len(content)) {
				t.Errorf("stat size = %d, want %d", info.Size, len(content))
			}
		})
	}
}

func TestGetMissingObject(t *testing.T) {
	ctx := context.Background()
	for name, build := range testStores(t) {
		t.Run(name, func(t *testing.T) {
			store := build(t)
			// A missing object is a normal outcome, not a failure: callers ask for
			// artifacts before a job may have produced them.
			if _, _, err := store.Get(ctx, "probe/missing.txt"); !errors.Is(err, ErrNotFound) {
				t.Errorf("get missing = %v, want ErrNotFound", err)
			}
			if _, err := store.Stat(ctx, "probe/missing.txt"); !errors.Is(err, ErrNotFound) {
				t.Errorf("stat missing = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestListAndRemove(t *testing.T) {
	ctx := context.Background()
	for name, build := range testStores(t) {
		t.Run(name, func(t *testing.T) {
			store := build(t)
			for _, key := range []string{"listing/a.txt", "listing/b.txt", "other/c.txt"} {
				content := []byte(key)
				if _, err := store.Put(ctx, key, bytes.NewReader(content), int64(len(content)), ""); err != nil {
					t.Fatalf("put %s: %v", key, err)
				}
			}

			objects, err := store.List(ctx, "listing/", 10)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(objects) != 2 {
				t.Fatalf("listed %d objects under the prefix, want 2: %+v", len(objects), objects)
			}
			for _, object := range objects {
				if !strings.HasPrefix(object.Key, "listing/") {
					t.Errorf("object %q does not belong to the prefix", object.Key)
				}
			}

			if err := store.Remove(ctx, "listing/a.txt"); err != nil {
				t.Fatalf("remove: %v", err)
			}
			if _, err := store.Stat(ctx, "listing/a.txt"); !errors.Is(err, ErrNotFound) {
				t.Errorf("the object still exists after removal")
			}

			// Removing something that is already gone must succeed: cleanup runs
			// after failures, and a missing object is the desired end state.
			if err := store.Remove(ctx, "listing/a.txt"); err != nil {
				t.Errorf("removing a missing object returned %v", err)
			}
		})
	}
}

func TestPutRejectsSizeMismatch(t *testing.T) {
	ctx := context.Background()
	local, err := NewLocal(t.TempDir(), "dogit")
	if err != nil {
		t.Fatal(err)
	}

	content := []byte("short")
	// The declared size is checked against what actually arrived, so a truncated
	// upload cannot be mistaken for a complete artifact.
	if _, err := local.Put(ctx, "probe/short.txt", bytes.NewReader(content), 999, ""); err == nil {
		t.Error("expected a size mismatch to be reported")
	}
}

func TestKeysAreNamespaced(t *testing.T) {
	local, err := NewLocal(t.TempDir(), "dogit")
	if err != nil {
		t.Fatal(err)
	}

	if got := local.Key("probe/a.txt"); got != "dogit/probe/a.txt" {
		t.Errorf("Key() = %q", got)
	}
	if got := local.Prefix(); got != "dogit" {
		t.Errorf("Prefix() = %q", got)
	}
}

func TestTraversalIsRejected(t *testing.T) {
	ctx := context.Background()
	local, err := NewLocal(t.TempDir(), "dogit")
	if err != nil {
		t.Fatal(err)
	}

	// A key reaches this code from a URL or a job specification, so it must never
	// be able to name a file outside the store.
	if _, err := local.Put(ctx, "../escape.txt", strings.NewReader("x"), 1, ""); err == nil {
		t.Error("a traversing key was accepted by Put")
	}
	if _, err := local.Stat(ctx, "../../etc/passwd"); err == nil {
		t.Error("a traversing key was accepted by Stat")
	}
}
