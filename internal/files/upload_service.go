package files

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/objectstore"
)

const uploadTTL = 24 * time.Hour

const cleanupGrace = time.Hour

type sessionGate struct {
	mu      sync.RWMutex
	slots   chan struct{}
	refs    int
	partsMu sync.Mutex
	parts   map[int]*partGate
}

type partGate struct {
	mu   sync.Mutex
	refs int
}

func (g *sessionGate) acquirePart(number int) (*sync.Mutex, func()) {
	g.partsMu.Lock()
	part := g.parts[number]
	if part == nil {
		part = &partGate{}
		g.parts[number] = part
	}
	part.refs++
	g.partsMu.Unlock()
	return &part.mu, func() {
		g.partsMu.Lock()
		part.refs--
		if part.refs == 0 && g.parts[number] == part {
			delete(g.parts, number)
		}
		g.partsMu.Unlock()
	}
}

type UploadService struct {
	store   UploadStore
	objects objectstore.Store
	policy  UploadPolicy
	now     func() time.Time
	gatesMu sync.Mutex
	gates   map[uuid.UUID]*sessionGate
}

func NewUploadService(store UploadStore, objects objectstore.Store, policy UploadPolicy, now func() time.Time) *UploadService {
	if now == nil {
		now = time.Now
	}
	if policy == nil {
		policy = TeachingUploadPolicy{}
	}
	return &UploadService{store: store, objects: objects, policy: policy, now: now, gates: make(map[uuid.UUID]*sessionGate)}
}

func (s *UploadService) Create(ctx context.Context, actor Principal, in CreateUploadInput) (UploadView, error) {
	if err := validatePrincipal(actor); err != nil {
		return UploadView{}, err
	}
	if err := s.policy.Authorize(actor.User); err != nil {
		return UploadView{}, err
	}
	if err := s.policy.Validate(in); err != nil {
		return UploadView{}, err
	}
	key, err := randomObjectKey()
	if err != nil {
		return UploadView{}, errors.New("generate upload object key")
	}
	uploadID, err := s.objects.CreateMultipart(ctx, key, objectstore.ObjectMeta{ContentType: in.DeclaredMIME, SHA256: in.ExpectedSHA256})
	if err != nil {
		return UploadView{}, errors.New("create upload storage")
	}
	now := s.now().UTC()
	session := UploadSession{ID: uuid.New(), ActorUserID: actor.User.ID, Purpose: s.policy.Purpose(), ObjectKey: key, MinIOUploadID: uploadID, DisplayName: in.DisplayName, DeclaredMIME: in.DeclaredMIME, ExpectedSize: in.ExpectedSize, ExpectedSHA256: in.ExpectedSHA256, State: UploadOpen, ExpiresAt: now.Add(uploadTTL), CreatedAt: now}
	if err := s.store.CreateSession(ctx, session); err != nil {
		abortErr := s.objects.AbortMultipart(context.WithoutCancel(ctx), key, uploadID)
		if abortErr != nil {
			return UploadView{}, errors.Join(errors.New("persist upload session"), abortErr)
		}
		return UploadView{}, errors.New("persist upload session")
	}
	return uploadView(session, nil), nil
}

func (s *UploadService) Status(ctx context.Context, actor Principal, id uuid.UUID) (UploadView, error) {
	if err := validatePrincipal(actor); err != nil {
		return UploadView{}, err
	}
	if err := s.policy.Authorize(actor.User); err != nil {
		return UploadView{}, err
	}
	if id == uuid.Nil {
		return UploadView{}, ErrInvalid
	}
	session, parts, err := s.store.GetSession(ctx, id, actor.User.ID, s.policy.Purpose())
	if err != nil {
		return UploadView{}, err
	}
	if session.State == UploadOpen && s.now().After(session.ExpiresAt) {
		return UploadView{}, ErrUploadExpired
	}
	return uploadView(session, parts), nil
}

func (s *UploadService) PutPart(ctx context.Context, actor Principal, in PutPartInput) (PartView, error) {
	if err := validatePrincipal(actor); err != nil {
		return PartView{}, err
	}
	if err := s.policy.Authorize(actor.User); err != nil {
		return PartView{}, err
	}
	if in.SessionID == uuid.Nil || in.Number < 1 || in.Number > 10000 || in.Size < 1 || !validHash(in.SHA256) || in.Body == nil {
		return PartView{}, ErrInvalid
	}
	gate, releaseGate := s.acquireGate(in.SessionID)
	defer releaseGate()
	gate.mu.RLock()
	defer gate.mu.RUnlock()
	select {
	case gate.slots <- struct{}{}:
		defer func() { <-gate.slots }()
	default:
		return PartView{}, ErrTooManyPartRequests
	}
	partLock, releasePart := gate.acquirePart(in.Number)
	defer releasePart()
	partLock.Lock()
	defer partLock.Unlock()
	session, existing, err := s.store.AdmitPart(ctx, in.SessionID, actor.User.ID, s.policy.Purpose(), in.Number, in.Size, in.SHA256, s.now())
	if err != nil {
		return PartView{}, err
	}
	if existing != nil {
		return partView(*existing), nil
	}
	wantSize, ok := expectedPartSize(session.ExpectedSize, in.Number)
	if !ok || in.Size != wantSize {
		return PartView{}, ErrInvalid
	}
	digest := sha256.New()
	source := io.LimitReader(in.Body, in.Size+1)
	counter := &countingReader{Reader: io.TeeReader(source, digest)}
	exact := io.LimitReader(counter, in.Size)
	remotePart, err := s.objects.PutPart(ctx, session.ObjectKey, session.MinIOUploadID, in.Number, exact, in.Size, in.SHA256)
	if err != nil {
		return PartView{}, errors.New("store upload part")
	}
	if counter.n != in.Size {
		return PartView{}, ErrInvalid
	}
	one := []byte{0}
	if n, readErr := source.Read(one); n != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		return PartView{}, ErrInvalid
	}
	actualHash := hex.EncodeToString(digest.Sum(nil))
	if actualHash != in.SHA256 {
		return PartView{}, ErrPartHashMismatch
	}
	part, err := s.store.RecordPart(ctx, session.ID, UploadPart{SessionID: session.ID, Number: in.Number, Size: in.Size, SHA256: actualHash, ETag: remotePart.ETag, CreatedAt: s.now().UTC()})
	if err != nil {
		return PartView{}, err
	}
	return partView(part), nil
}

func (s *UploadService) Complete(ctx context.Context, actor Principal, id uuid.UUID) (CompletedUpload, error) {
	if err := validatePrincipal(actor); err != nil {
		return CompletedUpload{}, err
	}
	if err := s.policy.Authorize(actor.User); err != nil {
		return CompletedUpload{}, err
	}
	if id == uuid.Nil {
		return CompletedUpload{}, ErrInvalid
	}
	gate, releaseGate := s.acquireGate(id)
	defer releaseGate()
	gate.mu.Lock()
	defer gate.mu.Unlock()
	session, parts, completed, err := s.store.BeginCompletion(ctx, id, actor.User.ID, s.policy.Purpose(), s.now())
	if err != nil {
		return CompletedUpload{}, err
	}
	if completed != nil {
		return *completed, nil
	}
	objectParts, err := validateCompletionParts(session, parts)
	if err != nil {
		_ = s.store.ReopenCompletion(ctx, session.ID)
		return CompletedUpload{}, err
	}
	info, err := s.objects.CompleteMultipart(ctx, session.ObjectKey, session.MinIOUploadID, objectParts)
	if errors.Is(err, objectstore.ErrNotFound) {
		info, err = s.objects.Stat(ctx, session.ObjectKey)
	}
	if err != nil {
		_ = s.store.ReopenCompletion(ctx, session.ID)
		return CompletedUpload{}, errors.New("complete upload storage")
	}
	if info.Size != session.ExpectedSize {
		return CompletedUpload{}, s.rejectCompletedObject(ctx, actor, session)
	}
	reader, objectInfo, err := s.objects.Get(ctx, session.ObjectKey, nil)
	if err != nil {
		return CompletedUpload{}, errors.New("verify completed upload")
	}
	digest := sha256.New()
	written, copyErr := io.Copy(digest, io.LimitReader(reader, session.ExpectedSize+1))
	closeErr := reader.Close()
	if copyErr != nil || closeErr != nil {
		return CompletedUpload{}, errors.New("verify completed upload")
	}
	if written != session.ExpectedSize || objectInfo.Size != session.ExpectedSize || hex.EncodeToString(digest.Sum(nil)) != session.ExpectedSHA256 {
		return CompletedUpload{}, s.rejectCompletedObject(ctx, actor, session)
	}
	result, err := s.store.FinishCompletion(ctx, session, actor)
	if err != nil {
		return CompletedUpload{}, errors.New("persist completed upload")
	}
	return result, nil
}

func (s *UploadService) rejectCompletedObject(ctx context.Context, actor Principal, session UploadSession) error {
	compensationCtx := context.WithoutCancel(ctx)
	if _, err := s.store.CancelSession(compensationCtx, session.ID, actor.User.ID, s.policy.Purpose(), UploadCancelled); err != nil {
		return errors.New("cancel invalid completed upload")
	}
	if err := s.objects.Delete(compensationCtx, session.ObjectKey); err != nil && !errors.Is(err, objectstore.ErrNotFound) {
		return errors.New("remove invalid completed upload")
	}
	return ErrFinalHashMismatch
}

func (s *UploadService) Cancel(ctx context.Context, actor Principal, id uuid.UUID) error {
	if err := validatePrincipal(actor); err != nil {
		return err
	}
	if err := s.policy.Authorize(actor.User); err != nil {
		return err
	}
	if id == uuid.Nil {
		return ErrInvalid
	}
	gate, releaseGate := s.acquireGate(id)
	defer releaseGate()
	gate.mu.Lock()
	defer gate.mu.Unlock()
	session, err := s.store.CancelSession(ctx, id, actor.User.ID, s.policy.Purpose(), UploadCancelled)
	if err != nil {
		return err
	}
	if err := s.objects.AbortMultipart(ctx, session.ObjectKey, session.MinIOUploadID); err != nil && !errors.Is(err, objectstore.ErrNotFound) {
		return errors.New("cancel upload storage")
	}
	return nil
}

func (s *UploadService) CleanupExpired(ctx context.Context, limit int) error {
	if limit < 1 || limit > 1000 {
		return ErrInvalid
	}
	sessions, err := s.store.ClaimCleanup(ctx, s.now().Add(-cleanupGrace), limit)
	if err != nil {
		return errors.New("claim expired uploads")
	}
	failed := false
	for _, session := range sessions {
		candidateFailed := func() bool {
			gate, releaseGate := s.acquireGate(session.ID)
			defer releaseGate()
			gate.mu.Lock()
			defer gate.mu.Unlock()
			confirmed, err := s.store.ConfirmCleanup(ctx, session.ID)
			if err != nil {
				return true
			}
			if err := s.objects.AbortMultipart(ctx, confirmed.ObjectKey, confirmed.MinIOUploadID); err != nil && !errors.Is(err, objectstore.ErrNotFound) {
				return true
			}
			if err := s.objects.Delete(ctx, confirmed.ObjectKey); err != nil && !errors.Is(err, objectstore.ErrNotFound) {
				return true
			}
			return s.store.FinishCleanup(ctx, confirmed.ID) != nil
		}()
		failed = failed || candidateFailed
	}
	if failed {
		return errors.New("cleanup expired uploads")
	}
	return nil
}

func (s *UploadService) acquireGate(id uuid.UUID) (*sessionGate, func()) {
	s.gatesMu.Lock()
	gate := s.gates[id]
	if gate == nil {
		gate = &sessionGate{slots: make(chan struct{}, 2), parts: make(map[int]*partGate)}
		s.gates[id] = gate
	}
	gate.refs++
	s.gatesMu.Unlock()
	return gate, func() {
		s.gatesMu.Lock()
		gate.refs--
		if gate.refs == 0 && s.gates[id] == gate {
			delete(s.gates, id)
		}
		s.gatesMu.Unlock()
	}
}

type countingReader struct {
	io.Reader
	n int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.n += int64(n)
	return n, err
}

func validatePrincipal(actor Principal) error {
	if actor.User.ID == uuid.Nil || actor.User.Status != auth.StatusActive || strings.TrimSpace(actor.RequestID) == "" || actor.IP == nil {
		return ErrForbidden
	}
	return nil
}

func expectedPartSize(total int64, number int) (int64, bool) {
	if total < 1 || number < 1 {
		return 0, false
	}
	start := int64(number-1) * UploadPartSize
	if start >= total {
		return 0, false
	}
	remaining := total - start
	if remaining > UploadPartSize {
		return UploadPartSize, true
	}
	return remaining, true
}

func validateCompletionParts(session UploadSession, parts []UploadPart) ([]objectstore.Part, error) {
	sort.Slice(parts, func(i, j int) bool { return parts[i].Number < parts[j].Number })
	wantCount := int((session.ExpectedSize + UploadPartSize - 1) / UploadPartSize)
	if len(parts) != wantCount {
		return nil, ErrUploadIncomplete
	}
	result := make([]objectstore.Part, len(parts))
	var total int64
	for i, part := range parts {
		wantSize, ok := expectedPartSize(session.ExpectedSize, i+1)
		if !ok || part.Number != i+1 || part.Size != wantSize || !validHash(part.SHA256) || part.ETag == "" {
			return nil, ErrUploadIncomplete
		}
		total += part.Size
		result[i] = objectstore.Part{Number: part.Number, ETag: part.ETag, Size: part.Size}
	}
	if total != session.ExpectedSize {
		return nil, ErrUploadIncomplete
	}
	return result, nil
}

func validCreateInput(in CreateUploadInput) bool {
	if in.ExpectedSize < 1 || in.ExpectedSize > MaxUploadSize || !validHash(in.ExpectedSHA256) || !validDisplayName(in.DisplayName) {
		return false
	}
	ext := strings.ToLower(filepath.Ext(in.DisplayName))
	mimes := allowedUploadTypes[ext]
	for _, allowed := range mimes {
		if in.DeclaredMIME == allowed {
			return true
		}
	}
	return false
}

func validDisplayName(name string) bool {
	if name == "" || !utf8.ValidString(name) || len([]rune(name)) > 255 || strings.TrimSpace(name) != name || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00\r\n") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return filepath.Ext(name) != ""
}

func validHash(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func randomObjectKey() (string, error) {
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return fmt.Sprintf("originals/%s", hex.EncodeToString(random)), nil
}

func uploadView(session UploadSession, parts []UploadPart) UploadView {
	sort.Slice(parts, func(i, j int) bool { return parts[i].Number < parts[j].Number })
	view := UploadView{ID: session.ID, DisplayName: session.DisplayName, DeclaredMIME: session.DeclaredMIME, ExpectedSize: session.ExpectedSize, ExpectedSHA256: session.ExpectedSHA256, State: session.State, ExpiresAt: session.ExpiresAt, Parts: make([]PartView, len(parts))}
	for i, part := range parts {
		view.Parts[i] = partView(part)
	}
	return view
}

func partView(part UploadPart) PartView {
	return PartView{Number: part.Number, Size: part.Size, SHA256: part.SHA256}
}

var allowedUploadTypes = map[string][]string{
	".pdf":  {"application/pdf"},
	".docx": {"application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
	".xlsx": {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
	".pptx": {"application/vnd.openxmlformats-officedocument.presentationml.presentation"},
	".txt":  {"text/plain"}, ".md": {"text/markdown"},
	".jpg": {"image/jpeg"}, ".jpeg": {"image/jpeg"}, ".png": {"image/png"}, ".webp": {"image/webp"}, ".gif": {"image/gif"},
	".mp4": {"video/mp4"}, ".webm": {"video/webm"}, ".ogg": {"video/ogg"}, ".ogv": {"video/ogg"},
	".mov": {"video/quicktime"}, ".avi": {"video/x-msvideo"}, ".mkv": {"video/x-matroska"},
}
