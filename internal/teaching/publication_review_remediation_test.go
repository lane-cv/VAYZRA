package teaching

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPublicationReaderHasLeastAuthorityAndExpiresAfterCheck(t *testing.T) {
	lessonID := uuid.New()
	store := &fakeCatalogStore{draft: Draft{
		LessonID: lessonID, Title: "Lesson", BodyMarkdown: "Body", LockVersion: 1,
		Audience: Audience{Mode: AudienceAll},
	}}
	probe := &publicationReaderProbe{}
	service := NewService(store, probe, fixedTeachingClock)

	if _, err := service.Publish(context.Background(), adminTeachingPrincipal(), PublishInput{LessonID: lessonID, ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if probe.storeExposed || probe.postgresExposed {
		t.Fatal("publication checker could recover the transactional store from its reader")
	}
	if probe.reader == nil {
		t.Fatal("publication checker did not retain reader")
	}
	if _, err := probe.reader.PublicationBlockers(context.Background(), lessonID, 1); !errors.Is(err, ErrPublicationReaderExpired) {
		t.Fatalf("retained reader error = %v, want expired reader", err)
	}
}

type publicationReaderProbe struct {
	reader          PublicationReader
	storeExposed    bool
	postgresExposed bool
}

func (p *publicationReaderProbe) Check(_ context.Context, reader PublicationReader, _ Draft) error {
	p.reader = reader
	_, p.storeExposed = reader.(TxStore)
	_, p.postgresExposed = reader.(*PostgresStore)
	return nil
}

func TestPublicationCheckErrorClassificationAndRollback(t *testing.T) {
	infrastructureFailure := errors.New("readiness database unavailable")
	tests := []struct {
		name     string
		checkErr error
		want     error
	}{
		{name: "semantic blocker", checkErr: ErrNotPublishable, want: ErrNotPublishable},
		{name: "infrastructure failure", checkErr: infrastructureFailure, want: infrastructureFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lessonID := uuid.New()
			store := &fakeCatalogStore{draft: Draft{
				LessonID: lessonID, Title: "Lesson", BodyMarkdown: "Body", LockVersion: 1,
				Audience: Audience{Mode: AudienceAll},
			}}
			service := NewService(store, publicationErrorCheck{err: tt.checkErr}, fixedTeachingClock)

			_, err := service.Publish(context.Background(), adminTeachingPrincipal(), PublishInput{LessonID: lessonID, ExpectedVersion: 1})
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			if store.published || store.finalized || store.outboxKind != "" {
				t.Fatalf("publication side effects were not rolled back: %#v", store)
			}
		})
	}
}

type publicationErrorCheck struct{ err error }

func (c publicationErrorCheck) Check(context.Context, PublicationReader, Draft) error { return c.err }

func TestPersistedDraftRequiresCanonicalExternalURLs(t *testing.T) {
	base := SaveDraftInput{
		LessonID: uuid.New(), ExpectedVersion: 1, Title: "Lesson", BodyMarkdown: "Body",
		Audience:       Audience{Mode: AudienceAll},
		ExternalVideos: []ExternalVideo{{ID: uuid.New(), URL: "https://video.example.test/watch", Title: "Video"}},
	}
	if !validPersistedDraft(base) {
		t.Fatal("canonical persisted URL rejected")
	}
	for _, raw := range []string{
		"https://VIDEO.EXAMPLE.TEST/watch",
		"https://video.example.test:443/watch",
		"https://video.example.test/watch#",
	} {
		t.Run(raw, func(t *testing.T) {
			tampered := base
			tampered.ExternalVideos = append([]ExternalVideo(nil), base.ExternalVideos...)
			tampered.ExternalVideos[0].URL = raw
			if validPersistedDraft(tampered) {
				t.Fatalf("non-canonical persisted URL accepted: %q", raw)
			}
		})
	}
}

func TestValidatePublicationBody(t *testing.T) {
	valid := []string{
		"# 一次函数\n速度公式 $v=s/t$，且集合为 $A=\\{1,2\\}$。",
		"$$\\frac{-b \\pm \\sqrt{b^2-4ac}}{2a}$$",
		"\\begin{aligned}x+y&=3\\\\x-y&=1\\end{aligned}",
		"```go\nfmt.Println(`$notMath`)\n```\n正文 $x^2$",
		"~~~text\n{ literal } and $ literal\n~~~",
		"~~~\rCR-only fenced text\r~~~",
	}
	for i, body := range valid {
		if err := validatePublicationBody(body); err != nil {
			t.Fatalf("valid[%d] rejected: %v", i, err)
		}
	}

	invalid := []struct {
		name string
		body string
	}{
		{name: "control", body: "safe\x01unsafe"},
		{name: "escaped control", body: "safe\\\x01unsafe"},
		{name: "backtick fence", body: "```math\n$x$"},
		{name: "tilde fence", body: "~~~\ntext"},
		{name: "tilde fence after hard break", body: "\\\n~~~\ntext"},
		{name: "inline math", body: "solve $x+1"},
		{name: "display math", body: "$$x+1$"},
		{name: "paren math", body: `solve \(x+1`},
		{name: "unmatched closing brace", body: "x } y"},
		{name: "unclosed brace", body: "x { y"},
		{name: "environment mismatch", body: `\begin{aligned}x\end{matrix}`},
		{name: "environment unclosed", body: `\begin{aligned}x`},
		{name: "dangerous write command", body: `\write 18{shell}`},
		{name: "brace depth", body: strings.Repeat("{", maxPublicationBraceDepth+1) + strings.Repeat("}", maxPublicationBraceDepth+1)},
		{name: "math expression", body: "$" + strings.Repeat("x", maxPublicationMathRunes+1) + "$"},
		{name: "math command complexity", body: "$" + strings.Repeat(`\alpha`, maxPublicationMathCommands+1) + "$"},
		{name: "oversized math command", body: "$\\" + strings.Repeat("a", maxPublicationMathRunes+1) + "$"},
		{name: "body size", body: strings.Repeat("x", maxPublicationBodyRunes+1)},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			if err := validatePublicationBody(tt.body); err == nil {
				t.Fatal("invalid body accepted")
			}
		})
	}
}

func TestPublicationReaderQueryFailureIsPreserved(t *testing.T) {
	queryFailure := errors.New("publication blocker query failed")
	lessonID := uuid.New()
	store := &fakeCatalogStore{draft: Draft{
		LessonID: lessonID, Title: "Lesson", BodyMarkdown: "Body", LockVersion: 1,
		Audience: Audience{Mode: AudienceAll},
	}, blockersErr: queryFailure}
	service := NewService(store, publicationBlockerQueryCheck{}, fixedTeachingClock)

	_, err := service.Publish(context.Background(), adminTeachingPrincipal(), PublishInput{LessonID: lessonID, ExpectedVersion: 1})
	if !errors.Is(err, queryFailure) {
		t.Fatalf("error = %v, want query failure", err)
	}
	if store.published {
		t.Fatal("query failure did not stop publication")
	}
}

type publicationBlockerQueryCheck struct{}

func (publicationBlockerQueryCheck) Check(ctx context.Context, reader PublicationReader, draft Draft) error {
	_, err := reader.PublicationBlockers(ctx, draft.LessonID, draft.LockVersion)
	return err
}

func TestPublicationReadScopeInvalidationWaitsForInflightRead(t *testing.T) {
	source := &blockingPublicationReader{entered: make(chan struct{}), release: make(chan struct{})}
	reader := newPublicationReadScope(source)
	readDone := make(chan error, 1)
	go func() {
		_, err := reader.PublicationBlockers(context.Background(), uuid.New(), 1)
		readDone <- err
	}()
	select {
	case <-source.entered:
	case <-time.After(time.Second):
		t.Fatal("underlying publication read did not start")
	}
	invalidateDone := make(chan struct{})
	go func() {
		reader.invalidate()
		close(invalidateDone)
	}()

	invalidatedEarly := false
	select {
	case <-invalidateDone:
		invalidatedEarly = true
	case <-time.After(100 * time.Millisecond):
	}
	close(source.release)
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("in-flight read error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight read did not finish")
	}
	select {
	case <-invalidateDone:
	case <-time.After(time.Second):
		t.Fatal("invalidation did not finish after read")
	}
	if invalidatedEarly {
		t.Fatal("reader invalidated before the in-flight underlying read completed")
	}
	if _, err := reader.PublicationBlockers(context.Background(), uuid.New(), 1); !errors.Is(err, ErrPublicationReaderExpired) {
		t.Fatalf("post-invalidation read error = %v, want expired", err)
	}
}

type blockingPublicationReader struct {
	entered chan struct{}
	release chan struct{}
}

func (r *blockingPublicationReader) PublicationBlockers(context.Context, uuid.UUID, int64) ([]string, error) {
	close(r.entered)
	<-r.release
	return nil, nil
}

func TestPublicationReaderExpiresWhenCheckerPanics(t *testing.T) {
	lessonID := uuid.New()
	store := &fakeCatalogStore{draft: Draft{
		LessonID: lessonID, Title: "Lesson", BodyMarkdown: "Body", LockVersion: 1,
		Audience: Audience{Mode: AudienceAll},
	}}
	checker := &panickingPublicationCheck{}
	service := NewService(store, checker, fixedTeachingClock)
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _ = service.Publish(context.Background(), adminTeachingPrincipal(), PublishInput{LessonID: lessonID, ExpectedVersion: 1})
	}()
	if recovered == nil {
		t.Fatal("checker panic did not propagate")
	}
	if checker.reader == nil {
		t.Fatal("checker did not retain its reader")
	}
	if _, err := checker.reader.PublicationBlockers(context.Background(), lessonID, 1); !errors.Is(err, ErrPublicationReaderExpired) {
		t.Fatalf("reader after checker panic error = %v, want expired", err)
	}
}

type panickingPublicationCheck struct{ reader PublicationReader }

func (c *panickingPublicationCheck) Check(_ context.Context, reader PublicationReader, _ Draft) error {
	c.reader = reader
	panic("publication checker panic")
}

func TestPublicationMathStateBoundariesAndEnvironmentComplexity(t *testing.T) {
	environmentPair := `\begin{a}\end{a}`
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "environment budget boundary", body: "$" + strings.Repeat(environmentPair, maxPublicationMathCommands/2) + "$"},
		{name: "brace crosses math closure", body: `$x^{2$}`, wantErr: true},
		{name: "environment opens inside math and closes outside", body: `$\begin{aligned}x$\end{aligned}`, wantErr: true},
		{name: "environment opens outside math and closes inside", body: `\begin{aligned}$x\end{aligned}$`, wantErr: true},
		{name: "repeated environments exceed expression limits", body: "$" + strings.Repeat(environmentPair, maxPublicationMathCommands/2+1) + "$", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePublicationBody(tt.body)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validatePublicationBody error = %v, wantErr=%t", err, tt.wantErr)
			}
		})
	}
}

func TestSaveDraftRejectsDisallowedControlsByField(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SaveDraftInput)
	}{
		{name: "lesson title control", mutate: func(in *SaveDraftInput) { in.Title = "Les\x01son" }},
		{name: "lesson title newline", mutate: func(in *SaveDraftInput) { in.Title = "Les\nson" }},
		{name: "summary control", mutate: func(in *SaveDraftInput) { in.Summary = "Summary\x01" }},
		{name: "video title control", mutate: func(in *SaveDraftInput) { in.ExternalVideos[0].Title = "Vid\x01eo" }},
		{name: "video title tab", mutate: func(in *SaveDraftInput) { in.ExternalVideos[0].Title = "Vid\teo" }},
		{name: "video description control", mutate: func(in *SaveDraftInput) { in.ExternalVideos[0].Description = "Description\x01" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lessonID := uuid.New()
			store := &fakeCatalogStore{draft: Draft{LessonID: lessonID, Title: "Current", LockVersion: 1}}
			service := NewService(store, nil, fixedTeachingClock)
			input := SaveDraftInput{
				LessonID: lessonID, ExpectedVersion: 1, Title: "Lesson", Summary: "Summary", BodyMarkdown: "Body",
				Audience:       Audience{Mode: AudienceAll},
				ExternalVideos: []ExternalVideo{{ID: uuid.New(), URL: "https://video.example.test/watch", Title: "Video", Description: "Description"}},
			}
			tt.mutate(&input)
			if _, err := service.SaveDraft(context.Background(), adminTeachingPrincipal(), input); !errors.Is(err, ErrInvalid) {
				t.Fatalf("SaveDraft error = %v, want invalid", err)
			}
			if store.draft.LockVersion != 1 || store.draft.Title != "Current" {
				t.Fatalf("invalid draft reached store: %#v", store.draft)
			}
		})
	}
}

func TestSaveDraftAllowsLayoutWhitespaceInMultilineFields(t *testing.T) {
	lessonID := uuid.New()
	store := &fakeCatalogStore{draft: Draft{LessonID: lessonID, Title: "Current", LockVersion: 1}}
	service := NewService(store, nil, fixedTeachingClock)
	_, err := service.SaveDraft(context.Background(), adminTeachingPrincipal(), SaveDraftInput{
		LessonID: lessonID, ExpectedVersion: 1, Title: "Lesson", Summary: "Line 1\n\tLine 2", BodyMarkdown: "Body",
		Audience:       Audience{Mode: AudienceAll},
		ExternalVideos: []ExternalVideo{{ID: uuid.New(), URL: "https://video.example.test/watch", Title: "Video", Description: "Line 1\n\tLine 2"}},
	})
	if err != nil {
		t.Fatalf("SaveDraft rejected intended multiline whitespace: %v", err)
	}
}
