package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ewolf/dogit/internal/objects"
)

type storeManager struct {
	mu     sync.Mutex
	stores map[string]objects.Store
}

func newStoreManager() *storeManager {
	return &storeManager{stores: map[string]objects.Store{}}
}

func (m *storeManager) get(ctx context.Context, cfg config, settings map[string]any) (objects.Store, error) {
	backend := strings.ToLower(settingString(settings, "storage_backend", "filesystem"))
	root := settingString(settings, "storage_path", cfg.storageDir)
	endpoint := settingString(settings, "s3_endpoint", "")
	bucket := settingString(settings, "s3_bucket", "")
	region := settingString(settings, "s3_region", "us-east-1")
	accessKey := settingString(settings, "s3_access_key", "")
	secretKey := settingString(settings, "s3_secret_key", "")
	prefix := settingString(settings, "s3_prefix", defaultStoragePrefix)

	fingerprint, err := json.Marshal([]string{backend, root, endpoint, bucket, region, accessKey, secretKey, prefix})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(fingerprint)
	key := hex.EncodeToString(sum[:])

	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.stores[key]; ok {
		return existing, nil
	}

	var store objects.Store
	switch backend {
	case "", "filesystem", "local":
		created, err := objects.NewLocal(root, defaultStoragePrefix)
		if err != nil {
			return nil, fmt.Errorf("configure filesystem cache: %w", err)
		}
		store = created
	case "s3":
		created, err := objects.NewS3(objects.S3Options{
			Endpoint: endpoint, Bucket: bucket, Region: region,
			AccessKey: accessKey, SecretKey: secretKey, Prefix: prefix,
		})
		if err != nil {
			return nil, fmt.Errorf("configure S3 cache: %w", err)
		}
		checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := created.EnsureBucket(checkCtx); err != nil {
			return nil, fmt.Errorf("check S3 cache bucket: %w", err)
		}
		store = created
	default:
		return nil, fmt.Errorf("unknown storage backend %q", backend)
	}
	m.stores[key] = store
	return store, nil
}
