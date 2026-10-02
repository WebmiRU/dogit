package config

import "testing"

func TestObjectsBackendIsDerivedFromConfiguration(t *testing.T) {
	t.Run("no endpoint falls back to local", func(t *testing.T) {
		clearEnv(t)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.ObjectsBackend != "local" {
			t.Errorf("backend = %q, want local", cfg.ObjectsBackend)
		}
		if cfg.ObjectsLocalDir != cfg.DataDir+"/objects" {
			t.Errorf("local dir = %q", cfg.ObjectsLocalDir)
		}
	})

	t.Run("an endpoint selects the remote store", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("DOGIT_S3_ENDPOINT", "https://s3.example.com")
		t.Setenv("DOGIT_S3_ACCESS_KEY", "key")
		t.Setenv("DOGIT_S3_SECRET_KEY", "secret")
		t.Setenv("DOGIT_S3_BUCKET", "bucket")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		// The backend follows the credentials rather than a separate switch, so the
		// two cannot disagree.
		if cfg.ObjectsBackend != "s3" {
			t.Errorf("backend = %q, want s3", cfg.ObjectsBackend)
		}
	})
}

// A half-configured remote store must fail at start-up, not on the first upload.
func TestIncompleteS3ConfigurationIsRejected(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"endpoint without credentials", map[string]string{
			"DOGIT_S3_ENDPOINT": "https://s3.example.com",
		}},
		{"credentials without a bucket", map[string]string{
			"DOGIT_S3_ENDPOINT":   "https://s3.example.com",
			"DOGIT_S3_ACCESS_KEY": "key",
			"DOGIT_S3_SECRET_KEY": "secret",
		}},
		{"blank access key", map[string]string{
			"DOGIT_S3_ENDPOINT":   "https://s3.example.com",
			"DOGIT_S3_ACCESS_KEY": "   ",
			"DOGIT_S3_SECRET_KEY": "secret",
			"DOGIT_S3_BUCKET":     "bucket",
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Error("expected an incomplete remote configuration to be rejected")
			}
		})
	}
}

func TestObjectsBackendRejectsUnknownValues(t *testing.T) {
	clearEnv(t)
	t.Setenv("DOGIT_OBJECTS_BACKEND", "ftp")

	if _, err := Load(); err == nil {
		t.Error("expected an unknown backend to be rejected")
	}
}
