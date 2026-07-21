package objectstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
)

func TestEnsurePrivateBucketPoliciesChecksEveryBucket(t *testing.T) {
	client := &fakeBucketPolicyClient{policies: map[string]string{
		"private-originals": "",
		"private-previews":  "",
	}}
	if err := ensurePrivateBucketPolicies(context.Background(), client, []string{"private-originals", "private-previews"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(client.calls, ",") != "private-originals,private-previews" {
		t.Fatalf("checked buckets=%v", client.calls)
	}
}

func TestEnsurePrivateBucketPolicyTreatsMissingPolicyAsPrivate(t *testing.T) {
	client := &fakeBucketPolicyClient{errs: map[string]error{
		"private-originals": minio.ErrorResponse{Code: "NoSuchBucketPolicy", Message: "missing"},
	}}
	if err := ensurePrivateBucketPolicies(context.Background(), client, []string{"private-originals"}); err != nil {
		t.Fatalf("missing policy must mean private: %v", err)
	}
}

func TestEnsurePrivateBucketPolicyFailsClosedWithoutLeakingPolicy(t *testing.T) {
	const secretPolicy = `{"Statement":[{"Principal":"*","Action":"s3:GetObject"}],"Secret":"do-not-leak"}`
	for name, client := range map[string]*fakeBucketPolicyClient{
		"public policy":     {policies: map[string]string{"private-originals": secretPolicy}},
		"unexpected policy": {policies: map[string]string{"private-originals": "   "}},
		"lookup failure":    {errs: map[string]error{"private-originals": errors.New("backend detail do-not-leak")}},
	} {
		t.Run(name, func(t *testing.T) {
			err := ensurePrivateBucketPolicies(context.Background(), client, []string{"private-originals"})
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("err=%v, want ErrUnavailable", err)
			}
			if strings.Contains(err.Error(), "do-not-leak") || strings.Contains(err.Error(), "Statement") {
				t.Fatalf("error leaks policy/backend detail: %v", err)
			}
		})
	}
}

type fakeBucketPolicyClient struct {
	policies map[string]string
	errs     map[string]error
	calls    []string
}

func (f *fakeBucketPolicyClient) GetBucketPolicy(_ context.Context, bucket string) (string, error) {
	f.calls = append(f.calls, bucket)
	return f.policies[bucket], f.errs[bucket]
}
