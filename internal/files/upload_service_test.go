package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/objectstore"
)

func TestUploadCreateValidationAndCompensation(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	store := newFakeUploadStore()
	objects := newFakeObjects()
	svc := NewUploadService(store, objects, TeachingUploadPolicy{}, func() time.Time { return now })
	actor := uploadAdmin()
	valid := CreateUploadInput{DisplayName: "lesson.pdf", DeclaredMIME: "application/pdf", ExpectedSize: 3, ExpectedSHA256: digestOf([]byte("pdf"))}

	bad := []CreateUploadInput{
		{DisplayName: "lesson.zip", DeclaredMIME: "application/zip", ExpectedSize: 3, ExpectedSHA256: valid.ExpectedSHA256},
		{DisplayName: "../lesson.pdf", DeclaredMIME: "application/pdf", ExpectedSize: 3, ExpectedSHA256: valid.ExpectedSHA256},
		{DisplayName: "lesson.pdf", DeclaredMIME: "text/plain", ExpectedSize: 3, ExpectedSHA256: valid.ExpectedSHA256},
		{DisplayName: "lesson.pdf", DeclaredMIME: "application/pdf", ExpectedSize: 0, ExpectedSHA256: valid.ExpectedSHA256},
		{DisplayName: "lesson.pdf", DeclaredMIME: "application/pdf", ExpectedSize: MaxUploadSize + 1, ExpectedSHA256: valid.ExpectedSHA256},
		{DisplayName: "lesson.pdf", DeclaredMIME: "application/pdf", ExpectedSize: 3, ExpectedSHA256: "not-a-hash"},
	}
	for _, input := range bad {
		if _, err := svc.Create(context.Background(), actor, input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Create(%+v) err=%v, want invalid", input, err)
		}
	}
	nonAdmin := actor
	nonAdmin.User.Role = auth.RoleStudent
	if _, err := svc.Create(context.Background(), nonAdmin, valid); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin err=%v", err)
	}
	store.createErr = errors.New("database unavailable")
	if _, err := svc.Create(context.Background(), actor, valid); err == nil {
		t.Fatal("expected persistence failure")
	}
	if objects.abortCalls.Load() != 1 {
		t.Fatalf("abort calls=%d", objects.abortCalls.Load())
	}
}

func TestQAUploadRejectsBeforeMultipartAndCannotCrossPurpose(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store, objects := newFakeUploadStore(), newFakeObjects()
	svc := NewUploadService(store, objects, QAUploadPolicy{}, func() time.Time { return now })
	actor := uploadAdmin()
	actor.User.Role = auth.RoleStudent
	bad := CreateUploadInput{DisplayName: "archive.zip", DeclaredMIME: "application/zip", ExpectedSize: 1, ExpectedSHA256: digestOf([]byte("x"))}
	if _, err := svc.Create(context.Background(), actor, bad); !errors.Is(err, ErrFileTypeRejected) {
		t.Fatalf("rejected type err=%v", err)
	}
	if objects.createCalls.Load() != 0 {
		t.Fatalf("denied upload created multipart object: %d", objects.createCalls.Load())
	}
	teaching := store.seed(actor.User.ID, 1, digestOf([]byte("x")), now.Add(time.Hour))
	if _, err := svc.Status(context.Background(), actor, teaching.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-purpose status err=%v", err)
	}
}

func TestUploadPartExpirationIdempotencyConflictAndStreamingHash(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	store := newFakeUploadStore()
	objects := newFakeObjects()
	svc := NewUploadService(store, objects, TeachingUploadPolicy{}, func() time.Time { return now })
	actor := uploadAdmin()
	payload := []byte("part-data")
	session := store.seed(actor.User.ID, int64(len(payload)), digestOf(payload), now.Add(time.Hour))
	in := PutPartInput{SessionID: session.ID, Number: 1, Size: int64(len(payload)), SHA256: digestOf(payload), Body: bytes.NewReader(payload)}
	part, err := svc.PutPart(context.Background(), actor, in)
	if err != nil || part.Number != 1 || part.SHA256 != in.SHA256 {
		t.Fatalf("part=%+v err=%v", part, err)
	}
	objects.putCalls.Store(0)
	in.Body = bytes.NewReader(payload)
	if _, err := svc.PutPart(context.Background(), actor, in); err != nil || objects.putCalls.Load() != 0 {
		t.Fatalf("idempotent err=%v putCalls=%d", err, objects.putCalls.Load())
	}
	conflict := in
	conflict.SHA256 = digestOf([]byte("different"))
	conflict.Body = bytes.NewReader(payload)
	if _, err := svc.PutPart(context.Background(), actor, conflict); !errors.Is(err, ErrUploadPartConflict) {
		t.Fatalf("conflict err=%v", err)
	}
	expired := store.seed(actor.User.ID, 1, digestOf([]byte("x")), now.Add(-time.Second))
	if _, err := svc.PutPart(context.Background(), actor, PutPartInput{SessionID: expired.ID, Number: 1, Size: 1, SHA256: digestOf([]byte("x")), Body: bytes.NewReader([]byte("x"))}); !errors.Is(err, ErrUploadExpired) {
		t.Fatalf("expired err=%v", err)
	}
	wrong := store.seed(actor.User.ID, 3, digestOf([]byte("abc")), now.Add(time.Hour))
	if _, err := svc.PutPart(context.Background(), actor, PutPartInput{SessionID: wrong.ID, Number: 1, Size: 3, SHA256: digestOf([]byte("xyz")), Body: bytes.NewReader([]byte("abc"))}); !errors.Is(err, ErrPartHashMismatch) {
		t.Fatalf("hash mismatch err=%v", err)
	}
}

func TestUploadCompleteMissingPartDuplicateAndFinalHashCompensation(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	actor := uploadAdmin()
	t.Run("missing part", func(t *testing.T) {
		store, objects := newFakeUploadStore(), newFakeObjects()
		svc := NewUploadService(store, objects, TeachingUploadPolicy{}, func() time.Time { return now })
		s := store.seed(actor.User.ID, UploadPartSize+1, digestOf(make([]byte, UploadPartSize+1)), now.Add(time.Hour))
		store.parts[s.ID] = []UploadPart{{SessionID: s.ID, Number: 2, Size: 1, SHA256: digestOf([]byte{0}), ETag: "two"}}
		if _, err := svc.Complete(context.Background(), actor, s.ID); !errors.Is(err, ErrUploadIncomplete) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("duplicate completion", func(t *testing.T) {
		payload := []byte("complete")
		store, objects := newFakeUploadStore(), newFakeObjects()
		objects.data = payload
		svc := NewUploadService(store, objects, TeachingUploadPolicy{}, func() time.Time { return now })
		s := store.seed(actor.User.ID, int64(len(payload)), digestOf(payload), now.Add(time.Hour))
		store.parts[s.ID] = []UploadPart{{SessionID: s.ID, Number: 1, Size: int64(len(payload)), SHA256: digestOf(payload), ETag: "one"}}
		first, err := svc.Complete(context.Background(), actor, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		second, err := svc.Complete(context.Background(), actor, s.ID)
		if err != nil || second.FileVersionID != first.FileVersionID || objects.completeCalls.Load() != 1 {
			t.Fatalf("first=%+v second=%+v calls=%d err=%v", first, second, objects.completeCalls.Load(), err)
		}
	})
	t.Run("hash mismatch deletes object and cancels", func(t *testing.T) {
		store, objects := newFakeUploadStore(), newFakeObjects()
		objects.data = []byte("evil")
		svc := NewUploadService(store, objects, TeachingUploadPolicy{}, func() time.Time { return now })
		s := store.seed(actor.User.ID, 4, digestOf([]byte("good")), now.Add(time.Hour))
		store.parts[s.ID] = []UploadPart{{SessionID: s.ID, Number: 1, Size: 4, SHA256: digestOf([]byte("good")), ETag: "one"}}
		if _, err := svc.Complete(context.Background(), actor, s.ID); !errors.Is(err, ErrFinalHashMismatch) {
			t.Fatalf("err=%v", err)
		}
		if objects.deleteCalls.Load() != 1 || store.sessions[s.ID].State != UploadCancelled {
			t.Fatalf("deletes=%d state=%s", objects.deleteCalls.Load(), store.sessions[s.ID].State)
		}
	})
}

func TestUploadObjectFailureAndCleanupSkipsCompletedReferencedObjects(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	actor := uploadAdmin()
	store, objects := newFakeUploadStore(), newFakeObjects()
	svc := NewUploadService(store, objects, TeachingUploadPolicy{}, func() time.Time { return now })
	s := store.seed(actor.User.ID, 1, digestOf([]byte("x")), now.Add(time.Hour))
	objects.putErr = objectstore.ErrUnavailable
	if _, err := svc.PutPart(context.Background(), actor, PutPartInput{SessionID: s.ID, Number: 1, Size: 1, SHA256: digestOf([]byte("x")), Body: bytes.NewReader([]byte("x"))}); err == nil {
		t.Fatal("expected object error")
	}
	expired := store.seed(actor.User.ID, 1, digestOf([]byte("x")), now.Add(-2*time.Hour))
	completed := store.seed(actor.User.ID, 1, digestOf([]byte("x")), now.Add(-2*time.Hour))
	completed.State = UploadCompleted
	store.sessions[completed.ID] = completed
	referenced := store.seed(actor.User.ID, 1, digestOf([]byte("x")), now.Add(-2*time.Hour))
	store.referenced[referenced.ObjectKey] = true
	objects.abortCalls.Store(0)
	if err := svc.CleanupExpired(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	_, expiredExists := store.sessions[expired.ID]
	if objects.abortCalls.Load() != 1 || expiredExists || store.sessions[completed.ID].State != UploadCompleted || store.sessions[referenced.ID].State != UploadOpen {
		t.Fatalf("aborts=%d expiredExists=%t completed=%s referenced=%s", objects.abortCalls.Load(), expiredExists, store.sessions[completed.ID].State, store.sessions[referenced.ID].State)
	}
}

func uploadAdmin() Principal {
	return Principal{User: auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "upload-request", IP: net.ParseIP("192.0.2.10")}
}

func digestOf(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

type fakeUploadStore struct {
	mu             sync.Mutex
	sessions       map[uuid.UUID]UploadSession
	parts          map[uuid.UUID][]UploadPart
	completed      map[uuid.UUID]CompletedUpload
	referenced     map[string]bool
	createErr      error
	cancelFailures int
	confirmHook    func(uuid.UUID)
	getCalls       atomic.Int64
	admitCalls     atomic.Int64
}

func newFakeUploadStore() *fakeUploadStore {
	return &fakeUploadStore{sessions: map[uuid.UUID]UploadSession{}, parts: map[uuid.UUID][]UploadPart{}, completed: map[uuid.UUID]CompletedUpload{}, referenced: map[string]bool{}}
}
func (s *fakeUploadStore) seed(actor uuid.UUID, size int64, hash string, expires time.Time) UploadSession {
	u := UploadSession{ID: uuid.New(), ActorUserID: actor, Purpose: UploadPurposeTeaching, ObjectKey: "uploads/" + uuid.NewString(), MinIOUploadID: uuid.NewString(), DisplayName: "lesson.pdf", DeclaredMIME: "application/pdf", ExpectedSize: size, ExpectedSHA256: hash, State: UploadOpen, ExpiresAt: expires}
	s.sessions[u.ID] = u
	return u
}
func (s *fakeUploadStore) CreateSession(_ context.Context, u UploadSession) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.sessions[u.ID] = u
	return nil
}
func (s *fakeUploadStore) GetSession(_ context.Context, id, actor uuid.UUID, purpose UploadPurpose) (UploadSession, []UploadPart, error) {
	s.getCalls.Add(1)
	u, ok := s.sessions[id]
	if !ok || u.ActorUserID != actor || u.Purpose != purpose {
		return UploadSession{}, nil, ErrNotFound
	}
	return u, append([]UploadPart(nil), s.parts[id]...), nil
}
func (s *fakeUploadStore) AdmitPart(_ context.Context, id, actor uuid.UUID, purpose UploadPurpose, n int, size int64, hash string, now time.Time) (UploadSession, *UploadPart, error) {
	s.admitCalls.Add(1)
	u, p, e := s.GetSession(context.Background(), id, actor, purpose)
	if e != nil {
		return u, nil, e
	}
	if now.After(u.ExpiresAt) {
		return u, nil, ErrUploadExpired
	}
	if u.State != UploadOpen {
		return u, nil, ErrUploadConflict
	}
	for i := range p {
		if p[i].Number == n {
			if p[i].Size == size && p[i].SHA256 == hash {
				return u, &p[i], nil
			}
			return u, nil, ErrUploadPartConflict
		}
	}
	return u, nil, nil
}
func (s *fakeUploadStore) RecordPart(_ context.Context, id uuid.UUID, p UploadPart) (UploadPart, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, old := range s.parts[id] {
		if old.Number == p.Number {
			if old.Size == p.Size && old.SHA256 == p.SHA256 {
				return old, nil
			}
			return UploadPart{}, ErrUploadPartConflict
		}
	}
	s.parts[id] = append(s.parts[id], p)
	return p, nil
}
func (s *fakeUploadStore) BeginCompletion(_ context.Context, id, actor uuid.UUID, purpose UploadPurpose, now time.Time) (UploadSession, []UploadPart, *CompletedUpload, error) {
	u, p, e := s.GetSession(context.Background(), id, actor, purpose)
	if e != nil {
		return u, nil, nil, e
	}
	if u.State == UploadCompleted {
		c := s.completed[id]
		return u, p, &c, nil
	}
	if now.After(u.ExpiresAt) {
		return u, nil, nil, ErrUploadExpired
	}
	if u.State != UploadOpen && u.State != UploadCompleting {
		return u, nil, nil, ErrUploadConflict
	}
	u.State = UploadCompleting
	s.sessions[id] = u
	return u, p, nil, nil
}
func (s *fakeUploadStore) ReopenCompletion(_ context.Context, id uuid.UUID) error {
	u := s.sessions[id]
	if u.State == UploadCompleting {
		u.State = UploadOpen
		s.sessions[id] = u
	}
	return nil
}
func (s *fakeUploadStore) FinishCompletion(_ context.Context, u UploadSession, _ Principal) (CompletedUpload, error) {
	c := CompletedUpload{FileID: uuid.New(), FileVersionID: uuid.New(), ProcessingState: "pending_scan"}
	s.completed[u.ID] = c
	u.State = UploadCompleted
	s.sessions[u.ID] = u
	return c, nil
}
func (s *fakeUploadStore) CancelSession(_ context.Context, id, actor uuid.UUID, purpose UploadPurpose, state UploadState) (UploadSession, error) {
	if s.cancelFailures > 0 {
		s.cancelFailures--
		return UploadSession{}, errors.New("cancel persistence failed")
	}
	u, _, e := s.GetSession(context.Background(), id, actor, purpose)
	if e != nil {
		return u, e
	}
	u.State = state
	s.sessions[id] = u
	return u, nil
}
func (s *fakeUploadStore) ClaimCleanup(_ context.Context, cutoff time.Time, limit int) ([]UploadSession, error) {
	var out []UploadSession
	for _, u := range s.sessions {
		eligible := u.State == UploadOpen || u.State == UploadExpired || u.State == UploadCancelled || u.State == UploadCompleting
		if eligible && !u.ExpiresAt.After(cutoff) && !s.referenced[u.ObjectKey] {
			out = append(out, u)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}
func (s *fakeUploadStore) ConfirmCleanup(_ context.Context, id uuid.UUID) (UploadSession, error) {
	if s.confirmHook != nil {
		s.confirmHook(id)
	}
	u, ok := s.sessions[id]
	if !ok {
		return UploadSession{}, ErrNotFound
	}
	if s.referenced[u.ObjectKey] || (u.State != UploadOpen && u.State != UploadExpired && u.State != UploadCancelled && u.State != UploadCompleting) {
		return UploadSession{}, ErrUploadConflict
	}
	if u.State == UploadOpen || u.State == UploadCompleting {
		u.State = UploadExpired
		s.sessions[id] = u
	}
	return u, nil
}
func (s *fakeUploadStore) FinishCleanup(_ context.Context, id uuid.UUID) error {
	u, ok := s.sessions[id]
	if !ok {
		return ErrNotFound
	}
	if s.referenced[u.ObjectKey] || (u.State != UploadExpired && u.State != UploadCancelled) {
		return ErrUploadConflict
	}
	delete(s.parts, id)
	delete(s.sessions, id)
	return nil
}

type fakeObjects struct {
	data                                                          []byte
	putErr                                                        error
	abortErr                                                      error
	deleteErr                                                     error
	createCalls, abortCalls, putCalls, completeCalls, deleteCalls atomic.Int64
}

func newFakeObjects() *fakeObjects { return &fakeObjects{data: []byte("complete")} }
func (o *fakeObjects) CreateMultipart(context.Context, string, objectstore.ObjectMeta) (string, error) {
	o.createCalls.Add(1)
	return uuid.NewString(), nil
}
func (o *fakeObjects) PutPart(_ context.Context, _ string, _ string, n int, r io.Reader, size int64, _ string) (objectstore.Part, error) {
	o.putCalls.Add(1)
	if o.putErr != nil {
		return objectstore.Part{}, o.putErr
	}
	written, err := io.Copy(io.Discard, r)
	if err != nil {
		return objectstore.Part{}, err
	}
	if written != size {
		return objectstore.Part{}, errors.New("wrong size")
	}
	return objectstore.Part{Number: n, ETag: "etag", Size: size}, nil
}
func (o *fakeObjects) CompleteMultipart(context.Context, string, string, []objectstore.Part) (objectstore.ObjectInfo, error) {
	o.completeCalls.Add(1)
	return objectstore.ObjectInfo{Size: int64(len(o.data))}, nil
}
func (o *fakeObjects) AbortMultipart(context.Context, string, string) error {
	o.abortCalls.Add(1)
	return o.abortErr
}
func (o *fakeObjects) Stat(context.Context, string) (objectstore.ObjectInfo, error) {
	return objectstore.ObjectInfo{Size: int64(len(o.data))}, nil
}
func (o *fakeObjects) Get(context.Context, string, *objectstore.ByteRange) (io.ReadCloser, objectstore.ObjectInfo, error) {
	return io.NopCloser(bytes.NewReader(o.data)), objectstore.ObjectInfo{Size: int64(len(o.data))}, nil
}
func (o *fakeObjects) Put(context.Context, string, io.Reader, int64, objectstore.ObjectMeta) (objectstore.ObjectInfo, error) {
	return objectstore.ObjectInfo{}, nil
}
func (o *fakeObjects) Delete(context.Context, string) error {
	o.deleteCalls.Add(1)
	return o.deleteErr
}
func TestUploadPartRejectsThirdInFlightRequest(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	store := newFakeUploadStore()
	objects := &blockingObjects{fakeObjects: newFakeObjects(), entered: make(chan struct{}, 2), release: make(chan struct{})}
	svc := NewUploadService(store, objects, TeachingUploadPolicy{}, func() time.Time { return now })
	actor := uploadAdmin()
	firstPayload := bytes.Repeat([]byte{0}, int(UploadPartSize))
	firstHash := digestOf(firstPayload)
	finalPayload := []byte("x")
	finalHash := digestOf(finalPayload)
	session := store.seed(actor.User.ID, UploadPartSize+1, digestOf([]byte("whole-upload")), now.Add(time.Hour))
	put := func(number int, payload []byte, hash string) error {
		_, err := svc.PutPart(context.Background(), actor, PutPartInput{SessionID: session.ID, Number: number, Size: int64(len(payload)), SHA256: hash, Body: bytes.NewReader(payload)})
		return err
	}
	errs := make(chan error, 2)
	go func() { errs <- put(1, firstPayload, firstHash) }()
	go func() { errs <- put(2, finalPayload, finalHash) }()
	<-objects.entered
	<-objects.entered
	if err := put(1, firstPayload, firstHash); !errors.Is(err, ErrTooManyPartRequests) {
		t.Fatalf("third request err=%v", err)
	}
	close(objects.release)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}

type blockingObjects struct {
	*fakeObjects
	entered chan struct{}
	release chan struct{}
}

func (o *blockingObjects) PutPart(ctx context.Context, key, uploadID string, number int, reader io.Reader, size int64, hash string) (objectstore.Part, error) {
	o.entered <- struct{}{}
	select {
	case <-o.release:
	case <-ctx.Done():
		return objectstore.Part{}, ctx.Err()
	}
	return o.fakeObjects.PutPart(ctx, key, uploadID, number, reader, size, hash)
}
func TestUploadFinalHashCompensationLeavesCancelledCleanupCandidate(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	actor := uploadAdmin()
	store, objects := newFakeUploadStore(), newFakeObjects()
	objects.data = []byte("evil")
	objects.deleteErr = objectstore.ErrUnavailable
	svc := NewUploadService(store, objects, TeachingUploadPolicy{}, func() time.Time { return now })
	s := store.seed(actor.User.ID, 4, digestOf([]byte("good")), now.Add(time.Hour))
	store.parts[s.ID] = []UploadPart{{SessionID: s.ID, Number: 1, Size: 4, SHA256: digestOf([]byte("good")), ETag: "one"}}
	if _, err := svc.Complete(context.Background(), actor, s.ID); err == nil || store.sessions[s.ID].State != UploadCancelled {
		t.Fatalf("first err=%v state=%s", err, store.sessions[s.ID].State)
	}
	u := store.sessions[s.ID]
	u.ExpiresAt = now.Add(-2 * time.Hour)
	store.sessions[s.ID] = u
	objects.deleteErr = nil
	if err := svc.CleanupExpired(context.Background(), 100); err != nil || objects.deleteCalls.Load() != 2 {
		t.Fatalf("cleanup err=%v deletes=%d", err, objects.deleteCalls.Load())
	}
	if _, ok := store.sessions[s.ID]; ok {
		t.Fatal("successful corrupt-object cleanup retained session")
	}
}
func TestUploadCleanupRetriesFailedAbort(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	actor := uploadAdmin()
	store, objects := newFakeUploadStore(), newFakeObjects()
	s := store.seed(actor.User.ID, 1, digestOf([]byte("x")), now.Add(-2*time.Hour))
	objects.abortErr = objectstore.ErrUnavailable
	svc := NewUploadService(store, objects, TeachingUploadPolicy{}, func() time.Time { return now })
	if err := svc.CleanupExpired(context.Background(), 100); err == nil || store.sessions[s.ID].State != UploadExpired {
		t.Fatalf("first err=%v state=%s", err, store.sessions[s.ID].State)
	}
	objects.abortErr = nil
	if err := svc.CleanupExpired(context.Background(), 100); err != nil || objects.abortCalls.Load() != 2 {
		t.Fatalf("retry err=%v aborts=%d", err, objects.abortCalls.Load())
	}
	if _, ok := store.sessions[s.ID]; ok {
		t.Fatal("successful cleanup retained expired session")
	}
}
func TestUploadConflictingConcurrentPartNeverReachesObjectStore(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	store := newFakeUploadStore()
	objects := &blockingObjects{fakeObjects: newFakeObjects(), entered: make(chan struct{}, 2), release: make(chan struct{})}
	svc := NewUploadService(store, objects, TeachingUploadPolicy{}, func() time.Time { return now })
	actor := uploadAdmin()
	session := store.seed(actor.User.ID, 1, digestOf([]byte("x")), now.Add(time.Hour))
	errs := make(chan error, 2)
	go func() {
		_, err := svc.PutPart(context.Background(), actor, PutPartInput{SessionID: session.ID, Number: 1, Size: 1, SHA256: digestOf([]byte("x")), Body: bytes.NewReader([]byte("x"))})
		errs <- err
	}()
	<-objects.entered
	go func() {
		_, err := svc.PutPart(context.Background(), actor, PutPartInput{SessionID: session.ID, Number: 1, Size: 1, SHA256: digestOf([]byte("y")), Body: bytes.NewReader([]byte("y"))})
		errs <- err
	}()
	select {
	case <-objects.entered:
		close(objects.release)
		<-errs
		<-errs
		t.Fatal("conflicting concurrent retry reached object storage")
	case <-time.After(200 * time.Millisecond):
		close(objects.release)
	}
	first, second := <-errs, <-errs
	if first != nil && second != nil {
		t.Fatalf("both requests failed: %v / %v", first, second)
	}
	if !errors.Is(first, ErrUploadPartConflict) && !errors.Is(second, ErrUploadPartConflict) {
		t.Fatalf("missing conflict: %v / %v", first, second)
	}
	if objects.putCalls.Load() != 1 {
		t.Fatalf("object put calls=%d", objects.putCalls.Load())
	}
}
func TestUploadCreatePreservesAbortFailure(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	store, objects := newFakeUploadStore(), newFakeObjects()
	store.createErr = errors.New("database unavailable")
	objects.abortErr = objectstore.ErrUnavailable
	svc := NewUploadService(store, objects, TeachingUploadPolicy{}, func() time.Time { return now })
	_, err := svc.Create(context.Background(), uploadAdmin(), CreateUploadInput{DisplayName: "lesson.pdf", DeclaredMIME: "application/pdf", ExpectedSize: 3, ExpectedSHA256: digestOf([]byte("pdf"))})
	if !errors.Is(err, objectstore.ErrUnavailable) || objects.abortCalls.Load() != 1 {
		t.Fatalf("err=%v aborts=%d", err, objects.abortCalls.Load())
	}
}
func TestUploadMismatchCancellationPersistsBeforeDeleteAndRetries(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	actor := uploadAdmin()
	store, objects := newFakeUploadStore(), newFakeObjects()
	store.cancelFailures = 1
	objects.data = []byte("evil")
	svc := NewUploadService(store, objects, TeachingUploadPolicy{}, func() time.Time { return now })
	s := store.seed(actor.User.ID, 4, digestOf([]byte("good")), now.Add(time.Hour))
	store.parts[s.ID] = []UploadPart{{SessionID: s.ID, Number: 1, Size: 4, SHA256: digestOf([]byte("good")), ETag: "one"}}
	if _, err := svc.Complete(context.Background(), actor, s.ID); err == nil {
		t.Fatal("expected cancellation persistence failure")
	}
	if objects.deleteCalls.Load() != 0 || store.sessions[s.ID].State != UploadCompleting {
		t.Fatalf("first deleteCalls=%d state=%s", objects.deleteCalls.Load(), store.sessions[s.ID].State)
	}
	if _, err := svc.Complete(context.Background(), actor, s.ID); !errors.Is(err, ErrFinalHashMismatch) {
		t.Fatalf("retry err=%v", err)
	}
	if objects.deleteCalls.Load() != 1 || store.sessions[s.ID].State != UploadCancelled {
		t.Fatalf("retry deleteCalls=%d state=%s", objects.deleteCalls.Load(), store.sessions[s.ID].State)
	}
}
func TestUploadCleanupGraceAndRecoveryStates(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	actor := uploadAdmin()
	t.Run("grace boundary", func(t *testing.T) {
		store, objects := newFakeUploadStore(), newFakeObjects()
		withinGrace := store.seed(actor.User.ID, 1, digestOf([]byte("x")), now.Add(-cleanupGrace+time.Second))
		svc := NewUploadService(store, objects, TeachingUploadPolicy{}, func() time.Time { return now })
		if err := svc.CleanupExpired(context.Background(), 100); err != nil {
			t.Fatal(err)
		}
		if objects.abortCalls.Load() != 0 || objects.deleteCalls.Load() != 0 || store.sessions[withinGrace.ID].State != UploadOpen {
			t.Fatalf("within grace aborts=%d deletes=%d state=%s", objects.abortCalls.Load(), objects.deleteCalls.Load(), store.sessions[withinGrace.ID].State)
		}
		store.sessions[withinGrace.ID] = func() UploadSession {
			u := store.sessions[withinGrace.ID]
			u.ExpiresAt = now.Add(-cleanupGrace)
			return u
		}()
		if err := svc.CleanupExpired(context.Background(), 100); err != nil {
			t.Fatal(err)
		}
		if objects.abortCalls.Load() != 1 || objects.deleteCalls.Load() != 1 {
			t.Fatalf("boundary aborts=%d deletes=%d", objects.abortCalls.Load(), objects.deleteCalls.Load())
		}
	})

	t.Run("stale completing", func(t *testing.T) {
		store, objects := newFakeUploadStore(), newFakeObjects()
		u := store.seed(actor.User.ID, 1, digestOf([]byte("x")), now.Add(-2*time.Hour))
		u.State = UploadCompleting
		store.sessions[u.ID] = u
		svc := NewUploadService(store, objects, TeachingUploadPolicy{}, func() time.Time { return now })
		if err := svc.CleanupExpired(context.Background(), 100); err != nil {
			t.Fatal(err)
		}
		if objects.abortCalls.Load() != 1 || objects.deleteCalls.Load() != 1 {
			t.Fatalf("aborts=%d deletes=%d", objects.abortCalls.Load(), objects.deleteCalls.Load())
		}
	})

	t.Run("cancelled delete retry", func(t *testing.T) {
		store, objects := newFakeUploadStore(), newFakeObjects()
		u := store.seed(actor.User.ID, 1, digestOf([]byte("x")), now.Add(-2*time.Hour))
		u.State = UploadCancelled
		store.sessions[u.ID] = u
		objects.deleteErr = objectstore.ErrUnavailable
		svc := NewUploadService(store, objects, TeachingUploadPolicy{}, func() time.Time { return now })
		if err := svc.CleanupExpired(context.Background(), 100); err == nil {
			t.Fatal("expected delete failure")
		}
		if _, ok := store.sessions[u.ID]; !ok {
			t.Fatal("failed cleanup removed retry candidate")
		}
		objects.deleteErr = nil
		if err := svc.CleanupExpired(context.Background(), 100); err != nil {
			t.Fatal(err)
		}
		if _, ok := store.sessions[u.ID]; ok {
			t.Fatal("successful cleanup retained candidate")
		}
	})
}

func TestUploadGateRegistriesEvictIdleEntries(t *testing.T) {
	svc := NewUploadService(newFakeUploadStore(), newFakeObjects(), TeachingUploadPolicy{}, time.Now)
	for i := 0; i < 1000; i++ {
		id := uuid.New()
		gate, releaseGate := svc.acquireGate(id)
		for number := 1; number <= 10; number++ {
			partLock, releasePart := gate.acquirePart(number)
			partLock.Lock()
			partLock.Unlock()
			releasePart()
		}
		if len(gate.parts) != 0 {
			t.Fatalf("session %d retained %d idle part gates", i, len(gate.parts))
		}
		releaseGate()
	}
	if len(svc.gates) != 0 {
		t.Fatalf("retained %d idle session gates", len(svc.gates))
	}
}

func TestUploadPartStreamsWithBoundedReaderRequests(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	actor := uploadAdmin()
	store, objects := newFakeUploadStore(), newFakeObjects()
	hash := sha256.New()
	if _, err := io.CopyN(hash, zeroReader{}, UploadPartSize); err != nil {
		t.Fatal(err)
	}
	expectedHash := hex.EncodeToString(hash.Sum(nil))
	session := store.seed(actor.User.ID, UploadPartSize, expectedHash, now.Add(time.Hour))
	probe := &boundedZeroReader{remaining: UploadPartSize, maxAllowed: 64 * 1024}
	svc := NewUploadService(store, objects, TeachingUploadPolicy{}, func() time.Time { return now })
	if _, err := svc.PutPart(context.Background(), actor, PutPartInput{
		SessionID: session.ID,
		Number:    1,
		Size:      UploadPartSize,
		SHA256:    expectedHash,
		Body:      probe,
	}); err != nil {
		t.Fatal(err)
	}
	if probe.read != UploadPartSize || probe.maxRequested > probe.maxAllowed {
		t.Fatalf("read=%d max request=%d", probe.read, probe.maxRequested)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

type boundedZeroReader struct {
	remaining    int64
	read         int64
	maxAllowed   int
	maxRequested int
}

func (r *boundedZeroReader) Read(p []byte) (int, error) {
	if len(p) > r.maxRequested {
		r.maxRequested = len(p)
	}
	if len(p) > r.maxAllowed {
		return 0, errors.New("reader requested an unbounded buffer")
	}
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > r.remaining {
		n = int(r.remaining)
	}
	clear(p[:n])
	r.remaining -= int64(n)
	r.read += int64(n)
	return n, nil
}

func TestUploadCleanupContinuesAfterCandidateFailure(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	actor := uploadAdmin()
	baseStore := newFakeUploadStore()
	first := baseStore.seed(actor.User.ID, 1, digestOf([]byte("x")), now.Add(-2*time.Hour))
	first.State = UploadExpired
	baseStore.sessions[first.ID] = first
	second := baseStore.seed(actor.User.ID, 1, digestOf([]byte("y")), now.Add(-2*time.Hour))
	second.State = UploadExpired
	baseStore.sessions[second.ID] = second
	store := &orderedCleanupStore{fakeUploadStore: baseStore, claimed: []UploadSession{first, second}}
	objects := &selectiveCleanupObjects{fakeObjects: newFakeObjects(), failKey: first.ObjectKey}
	svc := NewUploadService(store, objects, TeachingUploadPolicy{}, func() time.Time { return now })
	if err := svc.CleanupExpired(context.Background(), 100); err == nil {
		t.Fatal("expected opaque aggregate cleanup failure")
	}
	if _, ok := baseStore.sessions[first.ID]; !ok {
		t.Fatal("failed candidate was not retained for retry")
	}
	if _, ok := baseStore.sessions[second.ID]; ok {
		t.Fatal("later candidate was starved by earlier failure")
	}
	if objects.abortKeys[first.ObjectKey] != 1 || objects.abortKeys[second.ObjectKey] != 1 {
		t.Fatalf("abort attempts=%v", objects.abortKeys)
	}
}

type orderedCleanupStore struct {
	*fakeUploadStore
	claimed []UploadSession
}

func (s *orderedCleanupStore) ClaimCleanup(context.Context, time.Time, int) ([]UploadSession, error) {
	return append([]UploadSession(nil), s.claimed...), nil
}

type selectiveCleanupObjects struct {
	*fakeObjects
	failKey   string
	abortKeys map[string]int
}

func (o *selectiveCleanupObjects) AbortMultipart(ctx context.Context, key, uploadID string) error {
	if o.abortKeys == nil {
		o.abortKeys = make(map[string]int)
	}
	o.abortKeys[key]++
	if key == o.failKey {
		return objectstore.ErrUnavailable
	}
	return o.fakeObjects.AbortMultipart(ctx, key, uploadID)
}

func TestUploadCleanupConfirmsReferenceBeforeObjectDeletion(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	actor := uploadAdmin()
	store, objects := newFakeUploadStore(), newFakeObjects()
	candidate := store.seed(actor.User.ID, 1, digestOf([]byte("x")), now.Add(-2*time.Hour))
	store.confirmHook = func(id uuid.UUID) {
		if id != candidate.ID {
			t.Fatalf("confirmed wrong candidate %s", id)
		}
		store.referenced[candidate.ObjectKey] = true
	}
	svc := NewUploadService(store, objects, TeachingUploadPolicy{}, func() time.Time { return now })
	if err := svc.CleanupExpired(context.Background(), 100); err == nil {
		t.Fatal("expected cleanup confirmation conflict")
	}
	if objects.abortCalls.Load() != 0 || objects.deleteCalls.Load() != 0 {
		t.Fatalf("object operations crossed failed confirmation: abort=%d delete=%d", objects.abortCalls.Load(), objects.deleteCalls.Load())
	}
	if _, ok := store.sessions[candidate.ID]; !ok {
		t.Fatal("confirmation conflict removed retry metadata")
	}
}
