package objectstore

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/lifecycle"
)

const defaultOperationTimeout = 30 * time.Second

type MinIOConfig struct {
	Endpoint         string
	AccessKey        string
	SecretKey        string
	UseTLS           bool
	OriginalsBucket  string
	PreviewsBucket   string
	OperationTimeout time.Duration
}

type MinIOStores struct {
	Originals *MinIOStore
	Previews  *MinIOStore

	core             *minio.Core
	buckets          []string
	operationTimeout time.Duration
}

type MinIOStore struct {
	core             *minio.Core
	bucket           string
	operationTimeout time.Duration
}

func NewMinIO(ctx context.Context, cfg MinIOConfig) (*MinIOStores, error) {
	operationTimeout := cfg.OperationTimeout
	if operationTimeout <= 0 {
		operationTimeout = defaultOperationTimeout
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	core, err := minio.NewCore(cfg.Endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:       cfg.UseTLS,
		Transport:    transport,
		BucketLookup: minio.BucketLookupPath,
		MaxRetries:   1,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize object store: %w", ErrUnavailable)
	}
	stores := &MinIOStores{
		core:             core,
		buckets:          uniqueBuckets(cfg.OriginalsBucket, cfg.PreviewsBucket),
		operationTimeout: operationTimeout,
	}
	stores.Originals = &MinIOStore{core: core, bucket: cfg.OriginalsBucket, operationTimeout: operationTimeout}
	stores.Previews = &MinIOStore{core: core, bucket: cfg.PreviewsBucket, operationTimeout: operationTimeout}
	if err := stores.bootstrap(ctx); err != nil {
		return nil, err
	}
	return stores, nil
}

func uniqueBuckets(names ...string) []string {
	seen := make(map[string]struct{}, len(names))
	buckets := make([]string, 0, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		buckets = append(buckets, name)
	}
	return buckets
}

func (s *MinIOStores) bootstrap(ctx context.Context) error {
	if len(s.buckets) == 0 {
		return fmt.Errorf("bootstrap object store: %w", ErrUnavailable)
	}
	for _, bucket := range s.buckets {
		opCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
		exists, err := s.core.BucketExists(opCtx, bucket)
		cancel()
		if err != nil {
			return fmt.Errorf("bootstrap object store: %w", mapMinIOError(err))
		}
		if !exists {
			opCtx, cancel = context.WithTimeout(ctx, s.operationTimeout)
			err = s.core.MakeBucket(opCtx, bucket, minio.MakeBucketOptions{})
			cancel()
			if err != nil {
				response := minio.ToErrorResponse(err)
				if response.Code != "BucketAlreadyOwnedByYou" && response.Code != "BucketAlreadyExists" {
					return fmt.Errorf("bootstrap object store: %w", mapMinIOError(err))
				}
			}
		}
		opCtx, cancel = context.WithTimeout(ctx, s.operationTimeout)
		err = ensureIncompleteMultipartLifecycle(opCtx, s.core.Client, bucket)
		cancel()
		if err != nil {
			return fmt.Errorf("bootstrap object store: %w", err)
		}
	}
	return nil
}

type lifecycleClient interface {
	GetBucketLifecycle(context.Context, string) (*lifecycle.Configuration, error)
	SetBucketLifecycle(context.Context, string, *lifecycle.Configuration) error
}

const incompleteMultipartLifecycleRuleID = "happylearn-abort-incomplete-multipart"

func ensureIncompleteMultipartLifecycle(ctx context.Context, client lifecycleClient, bucket string) error {
	config, err := client.GetBucketLifecycle(ctx, bucket)
	if err != nil {
		response := minio.ToErrorResponse(err)
		if response.Code != "NoSuchLifecycleConfiguration" && response.Code != "NoSuchLifecycle" {
			return ErrUnavailable
		}
		config = &lifecycle.Configuration{}
	}
	if config == nil {
		config = &lifecycle.Configuration{}
	}
	if hasIncompleteMultipartLifecycle(config, 2) {
		return nil
	}
	rules := make([]lifecycle.Rule, 0, len(config.Rules)+1)
	for _, rule := range config.Rules {
		if rule.ID != incompleteMultipartLifecycleRuleID {
			rules = append(rules, rule)
		}
	}
	config.Rules = append(rules, canonicalIncompleteMultipartLifecycleRule())
	if err := client.SetBucketLifecycle(ctx, bucket, config); err != nil {
		return ErrUnavailable
	}
	verified, err := client.GetBucketLifecycle(ctx, bucket)
	if err != nil || !hasIncompleteMultipartLifecycle(verified, 2) {
		return ErrUnavailable
	}
	return nil
}

func canonicalIncompleteMultipartLifecycleRule() lifecycle.Rule {
	return lifecycle.Rule{
		ID:                             incompleteMultipartLifecycleRuleID,
		Status:                         "Enabled",
		AbortIncompleteMultipartUpload: lifecycle.AbortIncompleteMultipartUpload{DaysAfterInitiation: lifecycle.ExpirationDays(2)},
	}
}

func hasIncompleteMultipartLifecycle(config *lifecycle.Configuration, days lifecycle.ExpirationDays) bool {
	if config == nil {
		return false
	}
	reserved := 0
	for _, rule := range config.Rules {
		if rule.ID != incompleteMultipartLifecycleRuleID {
			continue
		}
		reserved++
		if !canonicalIncompleteMultipartLifecycle(rule, days) {
			return false
		}
	}
	return reserved == 1
}

func canonicalIncompleteMultipartLifecycle(rule lifecycle.Rule, days lifecycle.ExpirationDays) bool {
	return rule.ID == incompleteMultipartLifecycleRuleID &&
		rule.Status == "Enabled" &&
		rule.Prefix == "" &&
		rule.RuleFilter.IsNull() &&
		rule.AbortIncompleteMultipartUpload.DaysAfterInitiation == days &&
		rule.Expiration.IsNull() &&
		rule.DelMarkerExpiration.IsNull() &&
		rule.AllVersionsExpiration.IsNull() &&
		rule.NoncurrentVersionExpiration.NoncurrentDays == 0 &&
		rule.NoncurrentVersionExpiration.NewerNoncurrentVersions == 0 &&
		rule.NoncurrentVersionTransition.StorageClass == "" &&
		rule.NoncurrentVersionTransition.NoncurrentDays == 0 &&
		rule.NoncurrentVersionTransition.NewerNoncurrentVersions == 0 &&
		rule.Transition.IsNull()
}

func (s *MinIOStores) Ready(ctx context.Context) error {
	for _, bucket := range s.buckets {
		opCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
		exists, err := s.core.BucketExists(opCtx, bucket)
		cancel()
		if err != nil {
			return fmt.Errorf("check object store readiness: %w", mapMinIOError(err))
		}
		if !exists {
			return fmt.Errorf("check object store readiness: %w", ErrUnavailable)
		}
	}
	return nil
}

// DeleteBuckets removes only the two buckets configured for this instance.
// It is useful for disposing isolated integration resources and fails if a bucket is not empty.
func (s *MinIOStores) DeleteBuckets(ctx context.Context) error {
	for i := len(s.buckets) - 1; i >= 0; i-- {
		opCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
		err := s.core.RemoveBucket(opCtx, s.buckets[i])
		cancel()
		if err != nil && minio.ToErrorResponse(err).Code != "NoSuchBucket" {
			return fmt.Errorf("delete object store bucket: %w", mapMinIOError(err))
		}
	}
	return nil
}

func (s *MinIOStore) CreateMultipart(ctx context.Context, key string, meta ObjectMeta) (string, error) {
	opCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
	defer cancel()
	uploadID, err := s.core.NewMultipartUpload(opCtx, s.bucket, key, putOptions(meta))
	if err != nil {
		return "", fmt.Errorf("create multipart upload: %w", mapMinIOError(err))
	}
	return uploadID, nil
}

func (s *MinIOStore) PutPart(ctx context.Context, key, uploadID string, number int, reader io.Reader, size int64, sha256Hex string) (Part, error) {
	opCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
	defer cancel()
	part, err := s.core.PutObjectPart(opCtx, s.bucket, key, uploadID, number, reader, size, minio.PutObjectPartOptions{Sha256Hex: sha256Hex})
	if err != nil {
		return Part{}, fmt.Errorf("put multipart part: %w", mapMinIOError(err))
	}
	return Part{Number: part.PartNumber, ETag: part.ETag, Size: part.Size}, nil
}

func (s *MinIOStore) CompleteMultipart(ctx context.Context, key, uploadID string, parts []Part) (ObjectInfo, error) {
	completedParts := make([]minio.CompletePart, len(parts))
	for i, part := range parts {
		completedParts[i] = minio.CompletePart{PartNumber: part.Number, ETag: part.ETag}
	}
	opCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
	defer cancel()
	_, err := s.core.CompleteMultipartUpload(opCtx, s.bucket, key, uploadID, completedParts, minio.PutObjectOptions{})
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("complete multipart upload: %w", mapMinIOError(err))
	}
	info, err := s.core.StatObject(opCtx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("complete multipart upload: %w", mapMinIOError(err))
	}
	return objectInfo(info), nil
}

func (s *MinIOStore) AbortMultipart(ctx context.Context, key, uploadID string) error {
	opCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
	defer cancel()
	if err := s.core.AbortMultipartUpload(opCtx, s.bucket, key, uploadID); err != nil {
		return fmt.Errorf("abort multipart upload: %w", mapMinIOError(err))
	}
	return nil
}

func (s *MinIOStore) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	opCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
	defer cancel()
	info, err := s.core.StatObject(opCtx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("stat object: %w", mapMinIOError(err))
	}
	return objectInfo(info), nil
}

func (s *MinIOStore) Get(ctx context.Context, key string, byteRange *ByteRange) (io.ReadCloser, ObjectInfo, error) {
	if byteRange != nil && (byteRange.Offset < 0 || byteRange.Length <= 0 || byteRange.Offset > int64(^uint64(0)>>1)-byteRange.Length+1) {
		return nil, ObjectInfo{}, fmt.Errorf("get object: %w", ErrConflict)
	}
	opCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
	stat, err := s.core.StatObject(opCtx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		cancel()
		return nil, ObjectInfo{}, fmt.Errorf("get object: %w", mapMinIOError(err))
	}
	opts := minio.GetObjectOptions{}
	if byteRange != nil {
		if err := opts.SetRange(byteRange.Offset, byteRange.Offset+byteRange.Length-1); err != nil {
			cancel()
			return nil, ObjectInfo{}, fmt.Errorf("get object: %w", ErrConflict)
		}
	}
	reader, _, _, err := s.core.GetObject(opCtx, s.bucket, key, opts)
	if err != nil {
		cancel()
		return nil, ObjectInfo{}, fmt.Errorf("get object: %w", mapMinIOError(err))
	}
	return &cancelReadCloser{ReadCloser: reader, cancel: cancel}, objectInfo(stat), nil
}

func (s *MinIOStore) Put(ctx context.Context, key string, reader io.Reader, size int64, meta ObjectMeta) (ObjectInfo, error) {
	opCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
	defer cancel()
	result, err := s.core.PutObject(opCtx, s.bucket, key, reader, size, "", meta.SHA256, putOptions(meta))
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("put object: %w", mapMinIOError(err))
	}
	return ObjectInfo{Size: result.Size, ETag: result.ETag, LastModified: result.LastModified, ContentType: meta.ContentType}, nil
}

func (s *MinIOStore) Delete(ctx context.Context, key string) error {
	opCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
	defer cancel()
	if err := s.core.RemoveObject(opCtx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete object: %w", mapMinIOError(err))
	}
	return nil
}

func putOptions(meta ObjectMeta) minio.PutObjectOptions {
	metadata := make(map[string]string, len(meta.UserMetadata))
	for key, value := range meta.UserMetadata {
		metadata[key] = value
	}
	return minio.PutObjectOptions{ContentType: meta.ContentType, UserMetadata: metadata, DisableMultipart: true}
}

func objectInfo(info minio.ObjectInfo) ObjectInfo {
	return ObjectInfo{Size: info.Size, ETag: info.ETag, ContentType: info.ContentType, LastModified: info.LastModified}
}

type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *cancelReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.cancel()
	return err
}

func mapMinIOError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrUnavailable
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return ErrUnavailable
	}
	response := minio.ToErrorResponse(err)
	switch response.Code {
	case "NoSuchKey", "NoSuchObject", "NoSuchBucket", "NoSuchUpload", "NotFound":
		return ErrNotFound
	case "InvalidPart", "InvalidPartOrder", "EntityTooSmall", "BucketNotEmpty", "OperationAborted", "Conflict":
		return ErrConflict
	case "RequestTimeout", "SlowDown", "InternalError", "ServiceUnavailable":
		return ErrUnavailable
	}
	if response.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if response.StatusCode == http.StatusConflict {
		return ErrConflict
	}
	return ErrUnavailable
}
