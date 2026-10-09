package objects

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3 is the object store backed by any S3-compatible service: AWS S3 itself,
// Ceph Rados Gateway, MinIO, or a hosted provider.
//
// The client is created once and reused: every request signs with a date and a
// region, and constructing a client per call would make the credential lookup and
// the endpoint parsing dominate the actual transfer time.
type S3 struct {
	client *minio.Client
	bucket string
	prefix string
	// secure selects https; some self-hosted gateways only speak plain HTTP.
	secure bool
	// Part size for multipart uploads. Artifacts such as build caches are routinely
	// larger than any single-part limit, so uploads are split and a failure only
	// costs one part.
	partSize uint64

	once sync.Once
	err  error
}

// S3Options configures the remote store.
type S3Options struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	// Region is required by the signing algorithm even when the provider ignores it.
	Region string
	Prefix string
	// UseSSL selects https. Defaults to true when unset.
	UseSSL *bool
	// PartSizeMB overrides the multipart chunk size.
	PartSizeMB int
}

func NewS3(opts S3Options) (*S3, error) {
	if opts.Endpoint == "" || opts.Bucket == "" || opts.AccessKey == "" || opts.SecretKey == "" {
		return nil, fmt.Errorf("objects: endpoint, bucket and credentials are required")
	}
	if opts.Region == "" {
		opts.Region = "us-east-1"
	}

	// The endpoint may arrive as a full URL because that is how it is copied from a
	// provider's dashboard.
	endpoint := opts.Endpoint
	if parsed, err := url.Parse(endpoint); err == nil && parsed.Host != "" {
		endpoint = parsed.Host
		opts.UseSSL = boolPtr(parsed.Scheme != "http")
	}
	secure := true
	if opts.UseSSL != nil {
		secure = *opts.UseSSL
	}

	prefix := strings.Trim(opts.Prefix, "/")
	if prefix == "" {
		prefix = "dogit"
	}

	var partSize uint64 = 16 * 1024 * 1024
	if opts.PartSizeMB > 0 {
		partSize = uint64(opts.PartSizeMB) * 1024 * 1024
	}

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(opts.AccessKey, opts.SecretKey, opts.Region),
		Secure: secure,
		Region: opts.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("objects: cannot use endpoint %q: %w", endpoint, err)
	}

	return &S3{
		client:   client,
		bucket:   opts.Bucket,
		prefix:   prefix,
		secure:   secure,
		partSize: partSize,
	}, nil
}

func (s *S3) Prefix() string { return s.prefix }

// Key joins the namespace and a relative key.
func (s *S3) Key(relative string) string {
	return s.prefix + "/" + strings.TrimPrefix(relative, "/")
}

// EnsureBucket reports whether the bucket is reachable.
//
// It is called once at start-up so a misconfigured endpoint is discovered before
// the first push tries to upload an artifact, rather than in the middle of a job.
func (s *S3) EnsureBucket(ctx context.Context) error {
	s.once.Do(func() {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()

		exists, err := s.client.BucketExists(ctx, s.bucket)
		if err != nil {
			s.err = fmt.Errorf("objects: cannot reach bucket %q: %w", s.bucket, err)
			return
		}
		if !exists {
			s.err = fmt.Errorf("objects: bucket %q does not exist", s.bucket)
		}
	})
	return s.err
}

func (s *S3) Put(ctx context.Context, key string, content io.Reader, size int64, contentType string) (ObjectInfo, error) {
	full := s.Key(key)
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// An unknown size forces a streaming multipart upload, which is what a reader
	// of unknown length needs; a known size lets the client set Content-Length.
	// A size of -1 tells the client the length is unknown, which switches to a
	// streaming multipart upload. That is what a reader of unknown length needs,
	// and it avoids buffering a whole artifact in memory.
	info, err := s.client.PutObject(ctx, s.bucket, full, content, size, minio.PutObjectOptions{
		ContentType: contentType,
		PartSize:    s.partSize,
		NumThreads:  4,
	})
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("objects: put %q: %w", key, err)
	}

	return ObjectInfo{
		Key:          key,
		Size:         info.Size,
		LastModified: info.LastModified,
		ETag:         info.ETag,
		ContentType:  contentType,
	}, nil
}

func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	full := s.Key(key)

	info, err := s.client.StatObject(ctx, s.bucket, full, minio.StatObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return nil, ObjectInfo{}, ErrNotFound
		}
		return nil, ObjectInfo{}, fmt.Errorf("objects: stat %q: %w", key, err)
	}

	object, err := s.client.GetObject(ctx, s.bucket, full, minio.GetObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return nil, ObjectInfo{}, ErrNotFound
		}
		return nil, ObjectInfo{}, fmt.Errorf("objects: get %q: %w", key, err)
	}
	// StatObject already succeeded, so the object exists; without this check the
	// first Read would report a missing object as a generic error.
	if _, err := object.Stat(); err != nil {
		object.Close()
		if isNotFound(err) {
			return nil, ObjectInfo{}, ErrNotFound
		}
		return nil, ObjectInfo{}, fmt.Errorf("objects: get %q: %w", key, err)
	}

	return object, ObjectInfo{
		Key:          key,
		Size:         info.Size,
		LastModified: info.LastModified,
		ETag:         info.ETag,
		ContentType:  info.ContentType,
	}, nil
}

func (s *S3) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	info, err := s.client.StatObject(ctx, s.bucket, s.Key(key), minio.StatObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return ObjectInfo{}, ErrNotFound
		}
		return ObjectInfo{}, fmt.Errorf("objects: stat %q: %w", key, err)
	}
	return ObjectInfo{
		Key:          key,
		Size:         info.Size,
		LastModified: info.LastModified,
		ETag:         info.ETag,
		ContentType:  info.ContentType,
	}, nil
}

func (s *S3) List(ctx context.Context, prefix string, limit int) ([]ObjectInfo, error) {
	options := minio.ListObjectsOptions{Prefix: s.Key(prefix), Recursive: true}
	if limit > 0 {
		options.MaxKeys = limit
	}

	out := []ObjectInfo{}
	for object := range s.client.ListObjects(ctx, s.bucket, options) {
		if object.Err != nil {
			return nil, fmt.Errorf("objects: list %q: %w", prefix, object.Err)
		}
		out = append(out, ObjectInfo{
			Key:          strings.TrimPrefix(object.Key, s.prefix+"/"),
			Size:         object.Size,
			LastModified: object.LastModified,
			ETag:         object.ETag,
		})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *S3) Remove(ctx context.Context, key string) error {
	// Removing a missing object is not an error: callers clean up after failures
	// and races, and a missing object is the desired end state.
	err := s.client.RemoveObject(ctx, s.bucket, s.Key(key), minio.RemoveObjectOptions{})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("objects: remove %q: %w", key, err)
	}
	return nil
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	return minio.ToErrorResponse(err).Code == "NoSuchKey" ||
		minio.ToErrorResponse(err).Code == "NoSuchBucket" ||
		minio.ToErrorResponse(err).StatusCode == 404
}

func boolPtr(v bool) *bool { return &v }
