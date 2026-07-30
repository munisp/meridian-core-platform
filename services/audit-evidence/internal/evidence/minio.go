// MinIO WORM object-store backend (HARDENING H3). Selected when
// MINIO_ENDPOINT (+MINIO_ACCESS_KEY/MINIO_SECRET_KEY) is set. The bucket
// (MINIO_BUCKET, default meridian-evidence) is expected to have
// object-lock/WORM retention enabled (created with object locking when we
// create it). Evidence URIs use worm://minio/<bucket>/<key>; the sha256 of
// the content is stored as object user-metadata (sidecar) and in the
// document index.
package evidence

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/munisp/meridian-core-platform/packages/events/store"
)

// blobBackend abstracts where evidence bytes live.
type blobBackend interface {
	// Put stores content under key; returns the worm:// URI.
	Put(key string, content []byte, contentType string, sha256Hex string) (uri string, err error)
	// Get loads content by key.
	Get(key string) ([]byte, error)
}

// localBackend is the dev fallback: content-addressed files under dir.
type localBackend struct{ dir string }

func (b localBackend) Put(key string, content []byte, contentType string, _ string) (string, error) {
	path := b.dir + "/" + key
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o444); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	_ = os.Chmod(path, 0o444) // read-only at the FS level too
	return "worm://evidence/" + strings.TrimSuffix(key, ".bin"), nil
}

func (b localBackend) Get(key string) ([]byte, error) {
	return os.ReadFile(b.dir + "/" + key)
}

// minioBackend stores evidence in a MinIO WORM bucket.
type minioBackend struct {
	mc     *minio.Client
	bucket string
}

// newMinioBackendFromEnv builds the backend from MINIO_* env vars and
// ensures the WORM bucket exists (created with object-lock enabled).
func newMinioBackendFromEnv(ctx context.Context) (*minioBackend, error) {
	endpoint := os.Getenv("MINIO_ENDPOINT")
	if endpoint == "" {
		return nil, fmt.Errorf("MINIO_ENDPOINT required")
	}
	useSSL, _ := strconv.ParseBool(os.Getenv("MINIO_USE_SSL"))
	mc, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(os.Getenv("MINIO_ACCESS_KEY"), os.Getenv("MINIO_SECRET_KEY"), ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("minio: client: %w", err)
	}
	bucket := os.Getenv("MINIO_BUCKET")
	if bucket == "" {
		bucket = "meridian-evidence"
	}
	exists, err := mc.BucketExists(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("minio: bucket check: %w", err)
	}
	if !exists {
		if err := mc.MakeBucket(ctx, bucket, minio.MakeBucketOptions{ObjectLocking: true}); err != nil {
			return nil, fmt.Errorf("minio: create WORM bucket %s: %w", bucket, err)
		}
		log.Printf("minio: created object-locked bucket %s", bucket)
	}
	return &minioBackend{mc: mc, bucket: bucket}, nil
}

func (b *minioBackend) Put(key string, content []byte, contentType string, sha256Hex string) (string, error) {
	_, err := b.mc.PutObject(context.Background(), b.bucket, key, bytes.NewReader(content),
		int64(len(content)), minio.PutObjectOptions{
			ContentType:  contentType,
			UserMetadata: map[string]string{"sha256": sha256Hex, "worm": "true"},
		})
	if err != nil {
		return "", fmt.Errorf("minio: put %s/%s: %w", b.bucket, key, err)
	}
	return "worm://minio/" + b.bucket + "/" + key, nil
}

func (b *minioBackend) Get(key string) ([]byte, error) {
	obj, err := b.mc.GetObject(context.Background(), b.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(obj); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// NewWormStoreFromEnv selects the backend per HARDENING H1: MINIO_* set ->
// MinIO WORM bucket (profile=prod); otherwise the local WORM dir.
func NewWormStoreFromEnv(dir string, st store.DocStore) (*WormStore, error) {
	ws, err := NewWormStore(dir, st)
	if err != nil {
		return nil, err
	}
	if os.Getenv("MINIO_ENDPOINT") != "" {
		mb, err := newMinioBackendFromEnv(context.Background())
		if err != nil {
			log.Printf("profile=dev component=worm-store minio init failed (%v); local WORM dir", err)
		} else {
			log.Printf("profile=prod component=worm-store minio=%s bucket=%s",
				os.Getenv("MINIO_ENDPOINT"), mb.bucket)
			ws.backend = mb
			return ws, nil
		}
	} else {
		log.Printf("profile=dev component=worm-store local dir=%s", dir)
	}
	return ws, nil
}
