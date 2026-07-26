package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"happylearn.local/app/internal/platform/objectstore"
	"happylearn.local/app/internal/processing"
)

func TestHealthSeparatesLivenessAndReadiness(t *testing.T) {
	handler := healthHandler(func(context.Context) error { return errors.New("secret dependency detail") })
	live := httptest.NewRecorder()
	handler.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/live", nil))
	if live.Code != http.StatusOK || live.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("live status=%d headers=%v", live.Code, live.Header())
	}
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if ready.Code != http.StatusServiceUnavailable || strings.Contains(ready.Body.String(), "secret") {
		t.Fatalf("ready status=%d body=%q", ready.Code, ready.Body.String())
	}
}

func TestWorkerReadinessRequiresEveryDependency(t *testing.T) {
	checks := 0
	ready := workerReadiness(func(context.Context) error { checks++; return nil }, func(context.Context) error { checks++; return errors.New("storage") }, t.TempDir(), false, map[string][]string{"unused": {"--version"}})
	if err := ready(context.Background()); err == nil || checks != 2 || strings.Contains(err.Error(), "storage") {
		t.Fatalf("err=%v checks=%d", err, checks)
	}
}

func TestRequiredCommandsIncludePDFTextExtractor(t *testing.T) {
	args, ok := requiredCommands["pdftotext"]
	if !ok || len(args) == 0 {
		t.Fatalf("pdftotext readiness command=%v present=%t", args, ok)
	}
}

func TestPathOnTmpfsUsesMostSpecificMount(t *testing.T) {
	mountInfo := "1 0 0:1 / / rw - ext4 disk rw\n2 1 0:2 / /work rw - tmpfs tmpfs rw\n3 2 0:3 / /work/disk rw - ext4 disk rw\n"
	if !pathOnTmpfs("/work/jobs", strings.NewReader(mountInfo)) {
		t.Fatal("tmpfs child not detected")
	}
	if pathOnTmpfs("/work/disk/jobs", strings.NewReader(mountInfo)) {
		t.Fatal("nested disk incorrectly accepted as tmpfs")
	}
}

type leaseCountingStore struct {
	leases    int
	completed int
	job       *processing.Job
}

func (s *leaseCountingStore) LeaseNext(_ context.Context, owner string, now time.Time, duration time.Duration) (processing.Job, error) {
	s.leases++
	if s.job == nil {
		return processing.Job{}, processing.ErrNoJob
	}
	job := *s.job
	s.job = nil
	job.LeaseOwner = owner
	job.LeaseUntil = now.Add(duration)
	return job, nil
}
func (*leaseCountingStore) Heartbeat(context.Context, uuid.UUID, string, time.Time) error { return nil }
func (s *leaseCountingStore) Complete(context.Context, processing.Job, processing.Result) error {
	s.completed++
	return nil
}
func (*leaseCountingStore) Fail(context.Context, processing.Job, processing.Failure) error {
	return nil
}

func TestDefaultProcessorWiringFailsBeforeAnyLease(t *testing.T) {
	store := &leaseCountingStore{}
	worker, err := buildWorker(store, "staged-worker", func() (processing.Processor, error) { return newProductionProcessor(nil, nil, nil, "") })
	if err == nil || worker != nil || store.leases != 0 || strings.Contains(err.Error(), "clamscan") {
		t.Fatalf("worker=%v err=%v leases=%d", worker, err, store.leases)
	}
}

func TestProductionProcessorWiringReturnsPipelineForCompleteDependencies(t *testing.T) {
	processor, err := newProductionProcessor(sourceReaderStub{}, &workerBlobStub{}, &workerBlobStub{}, t.TempDir())
	if err != nil || processor == nil {
		t.Fatalf("processor=%v err=%v", processor, err)
	}
	pipeline, ok := processor.(*processing.Pipeline)
	if !ok {
		t.Fatalf("processor=%T", processor)
	}
	if pipeline.ClamDefinitionsDir != "/var/lib/clamav" {
		t.Fatalf("definitions=%q", pipeline.ClamDefinitionsDir)
	}
}

func TestBuildWorkerRunsOneInjectedProcessorLifecycle(t *testing.T) {
	job := processing.Job{ID: uuid.New(), FileVersionID: uuid.New(), Kind: processing.KindProcessFile, Attempts: 1}
	store := &leaseCountingStore{job: &job}
	worker, err := buildWorker(store, "test-worker", func() (processing.Processor, error) {
		return processorStub{}, nil
	})
	if err != nil || worker == nil || worker.Owner != "test-worker" || store.leases != 0 {
		t.Fatalf("worker=%v err=%v leases=%d", worker, err, store.leases)
	}
	worked, err := worker.RunOne(context.Background())
	if err != nil || !worked || store.leases != 1 || store.completed != 1 {
		t.Fatalf("worked=%t err=%v leases=%d completed=%d", worked, err, store.leases, store.completed)
	}
}

type processorStub struct{}

func (processorStub) Process(context.Context, processing.Job) (processing.Result, error) {
	return processing.Result{}, nil
}

type sourceReaderStub struct{}

func (sourceReaderStub) LoadSource(context.Context, uuid.UUID) (processing.SourceFile, error) {
	return processing.SourceFile{}, nil
}
func (sourceReaderStub) ReserveArtifact(context.Context, processing.ProcessingArtifact) error {
	return nil
}
func (sourceReaderStub) MarkArtifactStored(context.Context, string) error        { return nil }
func (sourceReaderStub) MarkArtifactDeletePending(context.Context, string) error { return nil }
func (sourceReaderStub) ForgetArtifact(context.Context, string) error            { return nil }

type workerBlobStub struct{}

func (*workerBlobStub) Get(context.Context, string, *objectstore.ByteRange) (io.ReadCloser, objectstore.ObjectInfo, error) {
	return io.NopCloser(strings.NewReader("")), objectstore.ObjectInfo{}, nil
}
func (*workerBlobStub) Put(context.Context, string, io.Reader, int64, objectstore.ObjectMeta) (objectstore.ObjectInfo, error) {
	return objectstore.ObjectInfo{}, nil
}
func (*workerBlobStub) Delete(context.Context, string) error { return nil }
func TestHealthAddressIsLoopbackOnlyAndShutdownBounded(t *testing.T) {
	if !strings.HasPrefix(workerHealthAddress, "127.0.0.1:") || workerShutdownLimit.String() != "20s" {
		t.Fatalf("address=%q shutdown=%s", workerHealthAddress, workerShutdownLimit)
	}
}
