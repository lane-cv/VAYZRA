package objectstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
)

func TestReadOnlyMinIOConstructorChecksExistingPrivateBucketsWithoutMutation(t *testing.T) {
	client := &fakeReadOnlyMinIOClient{
		exists: map[string]bool{
			"restore-originals": true,
			"restore-previews":  true,
		},
		policyErrors: map[string]error{
			"restore-originals": minio.ErrorResponse{Code: "NoSuchBucketPolicy"},
			"restore-previews":  minio.ErrorResponse{Code: "NoSuchBucketPolicy"},
		},
	}
	stores, err := newReadOnlyMinIOStores(
		context.Background(),
		MinIOConfig{
			OriginalsBucket: "restore-originals",
			PreviewsBucket:  "restore-previews",
		},
		client,
	)
	if err != nil {
		t.Fatal(err)
	}
	if stores.Originals == nil || stores.Previews == nil {
		t.Fatal("read-only stores are incomplete")
	}
	if strings.Join(client.calls, ",") !=
		"exists:restore-originals,policy:restore-originals,"+
			"exists:restore-previews,policy:restore-previews" {
		t.Fatalf("readiness calls=%v", client.calls)
	}
}

func TestReadOnlyMinIOConstructorFailsClosedForMissingPublicOrUnauthorizedBucket(t *testing.T) {
	const secret = "secret-access-key-and-backend-path"
	for _, testCase := range []struct {
		name   string
		client *fakeReadOnlyMinIOClient
	}{
		{
			name: "missing",
			client: &fakeReadOnlyMinIOClient{
				exists: map[string]bool{
					"restore-originals": true,
					"restore-previews":  false,
				},
				policyErrors: privateRestorePolicies(),
			},
		},
		{
			name: "public",
			client: &fakeReadOnlyMinIOClient{
				exists: map[string]bool{
					"restore-originals": true,
					"restore-previews":  true,
				},
				policies: map[string]string{
					"restore-previews": `{"Principal":"*","secret":"` + secret + `"}`,
				},
				policyErrors: map[string]error{
					"restore-originals": minio.ErrorResponse{Code: "NoSuchBucketPolicy"},
				},
			},
		},
		{
			name: "authorization",
			client: &fakeReadOnlyMinIOClient{
				existsErrors: map[string]error{
					"restore-originals": errors.New(secret),
				},
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := newReadOnlyMinIOStores(
				context.Background(),
				MinIOConfig{
					OriginalsBucket: "restore-originals",
					PreviewsBucket:  "restore-previews",
				},
				testCase.client,
			)
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("error=%v", err)
			}
			if strings.Contains(err.Error(), secret) ||
				strings.Contains(err.Error(), "Principal") {
				t.Fatalf("error leaks backend detail: %v", err)
			}
		})
	}
}

func TestReadOnlyMinIOStatMapsNotFoundAndHidesOperationalDetails(t *testing.T) {
	const secret = "private/object/key secret-access-key"
	client := &fakeReadOnlyMinIOClient{
		exists: map[string]bool{
			"restore-originals": true,
			"restore-previews":  true,
		},
		policyErrors: privateRestorePolicies(),
		stats: map[string]minio.ObjectInfo{
			"restore-originals/present": {Size: 41},
		},
		statErrors: map[string]error{
			"restore-originals/missing": minio.ErrorResponse{Code: "NoSuchKey"},
			"restore-originals/failed":  errors.New(secret),
		},
	}
	stores, err := newReadOnlyMinIOStores(
		context.Background(),
		MinIOConfig{
			OriginalsBucket: "restore-originals",
			PreviewsBucket:  "restore-previews",
		},
		client,
	)
	if err != nil {
		t.Fatal(err)
	}
	size, err := stores.Originals.Stat(context.Background(), "present")
	if err != nil || size != 41 {
		t.Fatalf("size=%d err=%v", size, err)
	}
	if _, err := stores.Originals.Stat(
		context.Background(),
		"missing",
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing error=%v", err)
	}
	if _, err := stores.Originals.Stat(
		context.Background(),
		"failed",
	); !errors.Is(err, ErrUnavailable) ||
		strings.Contains(err.Error(), secret) {
		t.Fatalf("operational error=%v", err)
	}
}

func privateRestorePolicies() map[string]error {
	return map[string]error{
		"restore-originals": minio.ErrorResponse{Code: "NoSuchBucketPolicy"},
		"restore-previews":  minio.ErrorResponse{Code: "NoSuchBucketPolicy"},
	}
}

type fakeReadOnlyMinIOClient struct {
	exists       map[string]bool
	existsErrors map[string]error
	policies     map[string]string
	policyErrors map[string]error
	stats        map[string]minio.ObjectInfo
	statErrors   map[string]error
	calls        []string
}

func (client *fakeReadOnlyMinIOClient) BucketExists(
	_ context.Context,
	bucket string,
) (bool, error) {
	client.calls = append(client.calls, "exists:"+bucket)
	return client.exists[bucket], client.existsErrors[bucket]
}

func (client *fakeReadOnlyMinIOClient) GetBucketPolicy(
	_ context.Context,
	bucket string,
) (string, error) {
	client.calls = append(client.calls, "policy:"+bucket)
	return client.policies[bucket], client.policyErrors[bucket]
}

func (client *fakeReadOnlyMinIOClient) StatObject(
	_ context.Context,
	bucket string,
	key string,
	_ minio.StatObjectOptions,
) (minio.ObjectInfo, error) {
	client.calls = append(client.calls, "stat:"+bucket+"/"+key)
	identity := bucket + "/" + key
	return client.stats[identity], client.statErrors[identity]
}
