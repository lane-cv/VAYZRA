package objectstore

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type readOnlyMinIOClient interface {
	BucketExists(context.Context, string) (bool, error)
	GetBucketPolicy(context.Context, string) (string, error)
	StatObject(
		context.Context,
		string,
		string,
		minio.StatObjectOptions,
	) (minio.ObjectInfo, error)
}

type ReadOnlyMinIOStores struct {
	Originals *ReadOnlyMinIOStore
	Previews  *ReadOnlyMinIOStore
}

type ReadOnlyMinIOStore struct {
	client           readOnlyMinIOClient
	bucket           string
	operationTimeout time.Duration
}

// NewReadOnlyMinIO constructs authenticated stores for restore verification.
// Its readiness path can only inspect existing buckets and policies; the
// deliberately narrow client interface exposes no bucket or lifecycle mutation.
func NewReadOnlyMinIO(
	ctx context.Context,
	cfg MinIOConfig,
) (*ReadOnlyMinIOStores, error) {
	if ctx == nil ||
		!validReadOnlyMinIOValue(cfg.Endpoint) ||
		!validReadOnlyMinIOValue(cfg.AccessKey) ||
		!validReadOnlyMinIOValue(cfg.SecretKey) {
		return nil, fmt.Errorf("initialize read-only object store: %w", ErrUnavailable)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:       cfg.UseTLS,
		Transport:    transport,
		BucketLookup: minio.BucketLookupPath,
		MaxRetries:   1,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize read-only object store: %w", ErrUnavailable)
	}
	return newReadOnlyMinIOStores(ctx, cfg, client)
}

func newReadOnlyMinIOStores(
	ctx context.Context,
	cfg MinIOConfig,
	client readOnlyMinIOClient,
) (*ReadOnlyMinIOStores, error) {
	if ctx == nil ||
		client == nil ||
		!validReadOnlyMinIOValue(cfg.OriginalsBucket) ||
		!validReadOnlyMinIOValue(cfg.PreviewsBucket) ||
		cfg.OriginalsBucket == cfg.PreviewsBucket {
		return nil, fmt.Errorf("initialize read-only object store: %w", ErrUnavailable)
	}
	operationTimeout := cfg.OperationTimeout
	if operationTimeout <= 0 {
		operationTimeout = defaultOperationTimeout
	}
	for _, bucket := range []string{
		cfg.OriginalsBucket,
		cfg.PreviewsBucket,
	} {
		opCtx, cancel := context.WithTimeout(ctx, operationTimeout)
		exists, err := client.BucketExists(opCtx, bucket)
		cancel()
		if err != nil || !exists {
			return nil, fmt.Errorf("check read-only object store: %w", ErrUnavailable)
		}
		opCtx, cancel = context.WithTimeout(ctx, operationTimeout)
		err = ensurePrivateBucketPolicies(
			opCtx,
			client,
			[]string{bucket},
		)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("check read-only object store: %w", ErrUnavailable)
		}
	}
	return &ReadOnlyMinIOStores{
		Originals: &ReadOnlyMinIOStore{
			client: client, bucket: cfg.OriginalsBucket,
			operationTimeout: operationTimeout,
		},
		Previews: &ReadOnlyMinIOStore{
			client: client, bucket: cfg.PreviewsBucket,
			operationTimeout: operationTimeout,
		},
	}, nil
}

func (store *ReadOnlyMinIOStore) Stat(
	ctx context.Context,
	key string,
) (int64, error) {
	if ctx == nil ||
		store == nil ||
		store.client == nil ||
		!validReadOnlyMinIOValue(key) {
		return 0, fmt.Errorf("stat read-only object: %w", ErrUnavailable)
	}
	opCtx, cancel := context.WithTimeout(ctx, store.operationTimeout)
	defer cancel()
	info, err := store.client.StatObject(
		opCtx,
		store.bucket,
		key,
		minio.StatObjectOptions{},
	)
	if err != nil {
		return 0, fmt.Errorf("stat read-only object: %w", mapMinIOError(err))
	}
	if info.Size < 0 {
		return 0, fmt.Errorf("stat read-only object: %w", ErrUnavailable)
	}
	return info.Size, nil
}

func validReadOnlyMinIOValue(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}
