package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	restoreProbeTeacherUsername = "restore-probe-teacher"
	restoreProbeTeacherPassword = "Restore Probe Teacher Password 42!"
	restoreProbeTeacherID       = "99999999-9999-4999-8999-999999999999"
	restoreProbeOtherTeacherID  = "88888888-8888-4888-8888-888888888888"
	restoreProbeStudentAID      = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	restoreProbeStudentBID      = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	restoreProbeThreadAID       = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	restoreProbeThreadBID       = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	restoreProbeMissingAID      = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	restoreProbeMissingBID      = "ffffffff-ffff-4fff-8fff-ffffffffffff"
)

func TestRunProgramDispatchesRestoreHTTPProbeBeforeBackupConstruction(t *testing.T) {
	actions := &recordingActions{}
	var actionConstructions, actionCloses, probeRuns int
	factories := programFactories{
		newActions: func(
			context.Context,
			func(string) string,
		) (commandActions, func(), error) {
			actionConstructions++
			return actions, func() { actionCloses++ }, nil
		},
		runRestoreHTTPProbe: func(context.Context) error {
			probeRuns++
			return nil
		},
	}
	if err := runProgram(
		context.Background(),
		[]string{"restore-http-probe"},
		func(string) string { return "" },
		factories,
	); err != nil {
		t.Fatal(err)
	}
	if actionConstructions != 0 || actionCloses != 0 || probeRuns != 1 {
		t.Fatalf(
			"constructions=%d closes=%d probe=%d",
			actionConstructions,
			actionCloses,
			probeRuns,
		)
	}

	for _, args := range [][]string{
		{"restore-http-probe", "--unexpected"},
		{"restore-http-probe", ""},
	} {
		if err := runProgram(
			context.Background(),
			args,
			func(string) string { return "" },
			factories,
		); !errors.Is(err, errInvalidCommand) {
			t.Fatalf("args=%q error=%v", args, err)
		}
	}
	if probeRuns != 1 || actionConstructions != 0 {
		t.Fatalf(
			"invalid dispatch constructions=%d probe=%d",
			actionConstructions,
			probeRuns,
		)
	}

	if err := runProgram(
		context.Background(),
		[]string{"prepare", "--run-id", commandRunID},
		func(string) string { return "" },
		factories,
	); err != nil {
		t.Fatal(err)
	}
	if probeRuns != 1 ||
		actionConstructions != 1 ||
		actionCloses != 1 ||
		len(actions.calls) != 1 ||
		actions.calls[0] != "prepare" {
		t.Fatalf(
			"ordinary constructions=%d closes=%d probe=%d calls=%v",
			actionConstructions,
			actionCloses,
			probeRuns,
			actions.calls,
		)
	}
}

func TestRunRestoreHTTPProbeExercisesTeacherAndTwoStudentIsolation(t *testing.T) {
	fixture := newRestoreHTTPProbeFixture(t, "")
	defer fixture.Close()
	if err := runRestoreHTTPProbe(
		context.Background(),
		restoreHTTPProbeConfig{
			baseURL:         fixture.URL() + "/api/v1",
			credentialsFile: fixture.CredentialsFile(),
			newUUID:         fixture.NextUUID,
		},
	); err != nil {
		t.Fatal(err)
	}
	fixture.AssertComplete(t)
}

func TestRunRestoreHTTPProbeFailsClosedForHTTPAndResponseViolations(
	t *testing.T,
) {
	for _, mode := range []string{
		"redirect",
		"malformed",
		"oversize",
		"login_401",
		"teacher_me_403",
		"teacher_id_mismatch",
		"cross_200",
		"bad_404",
	} {
		t.Run(mode, func(t *testing.T) {
			fixture := newRestoreHTTPProbeFixture(t, mode)
			defer fixture.Close()
			err := runRestoreHTTPProbe(
				context.Background(),
				restoreHTTPProbeConfig{
					baseURL:         fixture.URL() + "/api/v1",
					credentialsFile: fixture.CredentialsFile(),
					newUUID:         fixture.NextUUID,
				},
			)
			if !errors.Is(err, errRestoreHTTPProbeUnavailable) ||
				err.Error() != errRestoreHTTPProbeUnavailable.Error() {
				t.Fatalf("error=%v", err)
			}
			for _, forbidden := range []string{
				restoreProbeTeacherUsername,
				restoreProbeTeacherPassword,
				"teacher-session",
				restoreProbeStudentAID,
				restoreProbeThreadAID,
			} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("error leaked %q: %v", forbidden, err)
				}
			}
		})
	}
}

func TestLoadRestoreHTTPProbeCredentialsIsStrictBoundedAndSafe(t *testing.T) {
	valid := writeRestoreProbeCredentials(
		t,
		`{"username":"`+restoreProbeTeacherUsername+
			`","password":"`+restoreProbeTeacherPassword+`"}`,
		0o400,
	)
	credentials, err := loadRestoreHTTPProbeCredentials(valid)
	if err != nil ||
		credentials.Username != restoreProbeTeacherUsername ||
		credentials.Password != restoreProbeTeacherPassword {
		t.Fatalf("credentials=%+v error=%v", credentials, err)
	}

	for _, testCase := range []struct {
		name    string
		content string
		mode    os.FileMode
		mutate  func(*testing.T, string) string
	}{
		{
			name: "unknown",
			content: `{"username":"` + restoreProbeTeacherUsername +
				`","password":"` + restoreProbeTeacherPassword +
				`","token":"secret"}`,
			mode: 0o400,
		},
		{
			name: "duplicate",
			content: `{"username":"` + restoreProbeTeacherUsername +
				`","username":"other","password":"` +
				restoreProbeTeacherPassword + `"}`,
			mode: 0o400,
		},
		{
			name: "trailing",
			content: `{"username":"` + restoreProbeTeacherUsername +
				`","password":"` + restoreProbeTeacherPassword + `"} {}`,
			mode: 0o400,
		},
		{
			name:    "malformed",
			content: `{"username":"` + restoreProbeTeacherUsername + `"`,
			mode:    0o400,
		},
		{
			name: "oversize",
			content: `{"username":"` + restoreProbeTeacherUsername +
				`","password":"` + strings.Repeat("x", 4097) + `"}`,
			mode: 0o400,
		},
		{
			name: "wrong mode",
			content: `{"username":"` + restoreProbeTeacherUsername +
				`","password":"` + restoreProbeTeacherPassword + `"}`,
			mode: 0o600,
		},
		{
			name: "special mode bit",
			content: `{"username":"` + restoreProbeTeacherUsername +
				`","password":"` + restoreProbeTeacherPassword + `"}`,
			mode: 0o400 | os.ModeSetuid,
		},
		{
			name: "symlink",
			content: `{"username":"` + restoreProbeTeacherUsername +
				`","password":"` + restoreProbeTeacherPassword + `"}`,
			mode: 0o400,
			mutate: func(t *testing.T, path string) string {
				t.Helper()
				link := filepath.Join(t.TempDir(), "teacher.json")
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			path := writeRestoreProbeCredentials(
				t,
				testCase.content,
				testCase.mode,
			)
			if testCase.mutate != nil {
				path = testCase.mutate(t, path)
			}
			_, err := loadRestoreHTTPProbeCredentials(path)
			if !errors.Is(err, errRestoreHTTPProbeUnavailable) ||
				err.Error() != errRestoreHTTPProbeUnavailable.Error() {
				t.Fatalf("error=%v", err)
			}
			if strings.Contains(err.Error(), restoreProbeTeacherUsername) ||
				strings.Contains(err.Error(), restoreProbeTeacherPassword) {
				t.Fatalf("credential leak: %v", err)
			}
		})
	}
}

func TestRestoreHTTPProbeCredentialFileRequiresExactModeOwnerAndNoFollow(
	t *testing.T,
) {
	owned := restoreProbeCredentialFileInfo{
		mode: 0o400,
		stat: syscall.Stat_t{Uid: uint32(os.Geteuid())},
	}
	if !safeRestoreHTTPProbeCredentialInfo(owned) {
		t.Fatal("rejected exact owner-only regular file")
	}
	wrongOwner := owned
	wrongOwner.stat.Uid++
	if safeRestoreHTTPProbeCredentialInfo(wrongOwner) {
		t.Fatal("accepted credential owned by another UID")
	}
	specialMode := owned
	specialMode.mode |= os.ModeSetuid
	if safeRestoreHTTPProbeCredentialInfo(specialMode) {
		t.Fatal("accepted special credential mode bit")
	}

	target := writeRestoreProbeCredentials(
		t,
		`{"username":"`+restoreProbeTeacherUsername+
			`","password":"`+restoreProbeTeacherPassword+`"}`,
		0o400,
	)
	link := filepath.Join(t.TempDir(), "teacher.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	file, err := openRestoreHTTPProbeCredential(link)
	if file != nil {
		_ = file.Close()
	}
	if !errors.Is(err, errRestoreHTTPProbeUnavailable) {
		t.Fatalf("nofollow error=%v", err)
	}
}

func TestRunRestoreHTTPProbeHonorsCancellationAndTotalTimeout(t *testing.T) {
	credentialsFile := writeRestoreProbeCredentials(
		t,
		`{"username":"`+restoreProbeTeacherUsername+
			`","password":"`+restoreProbeTeacherPassword+`"}`,
		0o400,
	)
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			close(started)
			select {
			case <-r.Context().Done():
			case <-release:
			}
		},
	))
	defer func() {
		close(release)
		server.Close()
	}()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runRestoreHTTPProbe(ctx, restoreHTTPProbeConfig{
			baseURL:         server.URL + "/api/v1",
			credentialsFile: credentialsFile,
			totalTimeout:    time.Minute,
		})
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, errRestoreHTTPProbeUnavailable) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("probe ignored cancellation")
	}
}

func TestRunRestoreHTTPProbeDefaultTransportNeverUsesProxy(t *testing.T) {
	fixture := newRestoreHTTPProbeFixture(t, "")
	defer fixture.Close()
	address := strings.TrimPrefix(fixture.URL(), "http://")
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	oldDefaultTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldDefaultTransport })
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	var proxyCalls int
	http.DefaultTransport = &http.Transport{
		Proxy: func(request *http.Request) (*url.URL, error) {
			proxyCalls++
			return http.ProxyFromEnvironment(request)
		},
		DialContext: func(
			ctx context.Context,
			network string,
			_ string,
		) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	}
	err = runRestoreHTTPProbe(
		context.Background(),
		restoreHTTPProbeConfig{
			baseURL:         "http://app:" + port + "/api/v1",
			credentialsFile: fixture.CredentialsFile(),
			newUUID:         fixture.NextUUID,
		},
	)
	if err != nil || proxyCalls != 0 {
		t.Fatalf("error=%v proxy calls=%d", err, proxyCalls)
	}
	fixture.AssertComplete(t)
}

func writeRestoreProbeCredentials(
	t *testing.T,
	content string,
	mode os.FileMode,
) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "teacher.json")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

type restoreHTTPProbeFixture struct {
	t               *testing.T
	mode            string
	server          *httptest.Server
	credentialsFile string
	mu              sync.Mutex
	step            int
	students        [2]restoreProbeFixtureStudent
	uuids           []uuid.UUID
	uuidIndex       int
}

type restoreProbeCredentialFileInfo struct {
	mode os.FileMode
	stat syscall.Stat_t
}

func (info restoreProbeCredentialFileInfo) Name() string       { return "teacher.json" }
func (info restoreProbeCredentialFileInfo) Size() int64        { return 1 }
func (info restoreProbeCredentialFileInfo) Mode() os.FileMode  { return info.mode }
func (info restoreProbeCredentialFileInfo) ModTime() time.Time { return time.Time{} }
func (info restoreProbeCredentialFileInfo) IsDir() bool        { return false }
func (info restoreProbeCredentialFileInfo) Sys() any           { return &info.stat }

type restoreProbeFixtureStudent struct {
	id                string
	username          string
	temporaryPassword string
	newPassword       string
	threadID          string
}

func newRestoreHTTPProbeFixture(
	t *testing.T,
	mode string,
) *restoreHTTPProbeFixture {
	t.Helper()
	fixture := &restoreHTTPProbeFixture{
		t:    t,
		mode: mode,
		students: [2]restoreProbeFixtureStudent{
			{id: restoreProbeStudentAID, threadID: restoreProbeThreadAID},
			{id: restoreProbeStudentBID, threadID: restoreProbeThreadBID},
		},
		uuids: []uuid.UUID{
			uuid.MustParse("10000000-0000-4000-8000-000000000001"),
			uuid.MustParse("10000000-0000-4000-8000-000000000002"),
			uuid.MustParse("10000000-0000-4000-8000-000000000003"),
			uuid.MustParse("10000000-0000-4000-8000-000000000004"),
			uuid.MustParse(restoreProbeMissingAID),
			uuid.MustParse(restoreProbeMissingBID),
		},
	}
	fixture.credentialsFile = writeRestoreProbeCredentials(
		t,
		`{"username":"`+restoreProbeTeacherUsername+
			`","password":"`+restoreProbeTeacherPassword+`"}`,
		0o400,
	)
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.ServeHTTP))
	return fixture
}

func (fixture *restoreHTTPProbeFixture) Close() {
	fixture.server.Close()
}

func (fixture *restoreHTTPProbeFixture) URL() string {
	return fixture.server.URL
}

func (fixture *restoreHTTPProbeFixture) CredentialsFile() string {
	return fixture.credentialsFile
}

func (fixture *restoreHTTPProbeFixture) NextUUID() (uuid.UUID, error) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.uuidIndex >= len(fixture.uuids) {
		return uuid.Nil, errors.New("UUID fixture exhausted")
	}
	id := fixture.uuids[fixture.uuidIndex]
	fixture.uuidIndex++
	return id, nil
}

func (fixture *restoreHTTPProbeFixture) AssertComplete(t *testing.T) {
	t.Helper()
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.step != 20 {
		t.Fatalf("request step=%d want=20", fixture.step)
	}
	if fixture.students[0].username == "" ||
		fixture.students[1].username == "" ||
		fixture.students[0].username == fixture.students[1].username ||
		fixture.students[0].temporaryPassword == "" ||
		fixture.students[1].temporaryPassword == "" ||
		fixture.students[0].newPassword == "" ||
		fixture.students[1].newPassword == "" {
		t.Fatalf("students=%+v", fixture.students)
	}
}

func (fixture *restoreHTTPProbeFixture) ServeHTTP(
	w http.ResponseWriter,
	r *http.Request,
) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()

	step := fixture.step
	fixture.step++
	if r.Method == http.MethodPost {
		origins := r.Header.Values("Origin")
		if len(origins) != 1 || origins[0] != "http://app:8080" {
			fixture.t.Errorf("step=%d origin=%q", step, origins)
		}
	}
	if step == 0 {
		fixture.serveTeacherLogin(w, r)
		return
	}
	if fixture.mode == "teacher_me_403" && step == 1 {
		fixture.writeError(w, http.StatusForbidden, "forbidden", "无权访问", "req-forbidden")
		return
	}
	if fixture.mode == "teacher_id_mismatch" && step == 1 {
		fixture.requireRequest(r, http.MethodGet, "/api/v1/auth/me", "teacher-session", "teacher-csrf")
		fixture.writeUser(w, restoreProbeTeacherUsername, restoreProbeOtherTeacherID, "admin", false)
		return
	}
	switch step {
	case 1:
		fixture.requireRequest(r, http.MethodGet, "/api/v1/auth/me", "teacher-session", "teacher-csrf")
		fixture.writeUser(w, restoreProbeTeacherUsername, restoreProbeTeacherID, "admin", false)
	case 2, 3:
		fixture.serveCreateStudent(w, r, step-2)
	case 4:
		fixture.serveStudentLogin(w, r, 0)
	case 5:
		fixture.servePasswordChange(w, r, 0)
	case 6:
		fixture.serveStudentMe(w, r, 0)
	case 7:
		fixture.serveCreateThread(w, r, 0)
	case 8:
		fixture.serveStudentThread(w, r, 0, 0, http.StatusOK)
	case 9:
		fixture.serveStudentLogin(w, r, 1)
	case 10:
		fixture.servePasswordChange(w, r, 1)
	case 11:
		fixture.serveStudentMe(w, r, 1)
	case 12:
		fixture.serveCreateThread(w, r, 1)
	case 13:
		fixture.serveStudentThread(w, r, 1, 1, http.StatusOK)
	case 14, 15:
		fixture.serveTeacherThread(w, r, step-14)
	case 16:
		if fixture.mode == "cross_200" {
			fixture.serveStudentThread(w, r, 0, 1, http.StatusOK)
			return
		}
		fixture.serveNotFound(w, r, 0, fixture.students[1].threadID, "req-cross-a")
	case 17:
		fixture.serveNotFound(w, r, 0, restoreProbeMissingAID, "req-missing-a")
	case 18:
		fixture.serveNotFound(w, r, 1, fixture.students[0].threadID, "req-cross-b")
	case 19:
		fixture.serveNotFound(w, r, 1, restoreProbeMissingBID, "req-missing-b")
	default:
		fixture.t.Errorf("unexpected request step=%d %s %s", step, r.Method, r.URL.Path)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}
}

func (fixture *restoreHTTPProbeFixture) serveTeacherLogin(
	w http.ResponseWriter,
	r *http.Request,
) {
	fixture.requireRequest(r, http.MethodPost, "/api/v1/auth/login", "", "")
	if got := r.Header.Get("X-CSRF-Token"); got != "" {
		fixture.t.Errorf("login csrf=%q", got)
	}
	if fixture.mode == "redirect" {
		http.Redirect(w, r, "/api/v1/auth/me", http.StatusFound)
		return
	}
	if fixture.mode == "malformed" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"username":"` +
			restoreProbeTeacherUsername))
		return
	}
	if fixture.mode == "oversize" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"secret":"` +
			strings.Repeat("x", 70*1024) + `"}`))
		return
	}
	if fixture.mode == "login_401" {
		fixture.writeError(
			w,
			http.StatusUnauthorized,
			"invalid_credentials",
			restoreProbeTeacherPassword,
			"req-login",
		)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	fixture.decodeBody(r, &body)
	if body.Username != restoreProbeTeacherUsername ||
		body.Password != restoreProbeTeacherPassword {
		fixture.t.Errorf("teacher login body=%+v", body)
	}
	fixture.setCookies(w, "teacher-session", "teacher-csrf")
	fixture.writeUser(w, restoreProbeTeacherUsername, restoreProbeTeacherID, "admin", false)
}

func (fixture *restoreHTTPProbeFixture) serveCreateStudent(
	w http.ResponseWriter,
	r *http.Request,
	index int,
) {
	fixture.requireRequest(r, http.MethodPost, "/api/v1/admin/students", "teacher-session", "teacher-csrf")
	var body struct {
		Username          string `json:"username"`
		DisplayName       string `json:"displayName"`
		TemporaryPassword string `json:"temporaryPassword"`
	}
	fixture.decodeBody(r, &body)
	if body.Username == "" ||
		body.DisplayName == "" ||
		body.TemporaryPassword == "" {
		fixture.t.Errorf("student create body=%+v", body)
	}
	fixture.students[index].username = body.Username
	fixture.students[index].temporaryPassword = body.TemporaryPassword
	fixture.writeJSON(w, http.StatusCreated, map[string]any{
		"data": map[string]any{
			"id":                 fixture.students[index].id,
			"username":           body.Username,
			"displayName":        body.DisplayName,
			"status":             "active",
			"mustChangePassword": true,
			"createdAt":          "2026-07-29T00:00:00Z",
		},
	})
}

func (fixture *restoreHTTPProbeFixture) serveStudentLogin(
	w http.ResponseWriter,
	r *http.Request,
	index int,
) {
	fixture.requireRequest(r, http.MethodPost, "/api/v1/auth/login", "", "")
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	fixture.decodeBody(r, &body)
	student := fixture.students[index]
	if body.Username != student.username ||
		body.Password != student.temporaryPassword {
		fixture.t.Errorf("student %d login body=%+v", index, body)
	}
	fixture.setCookies(
		w,
		fmt.Sprintf("student-%d-before", index),
		fmt.Sprintf("student-%d-csrf-before", index),
	)
	fixture.writeUser(w, student.username, student.id, "student", true)
}

func (fixture *restoreHTTPProbeFixture) servePasswordChange(
	w http.ResponseWriter,
	r *http.Request,
	index int,
) {
	fixture.requireRequest(
		r,
		http.MethodPost,
		"/api/v1/auth/change-password",
		fmt.Sprintf("student-%d-before", index),
		fmt.Sprintf("student-%d-csrf-before", index),
	)
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	fixture.decodeBody(r, &body)
	if body.CurrentPassword != fixture.students[index].temporaryPassword ||
		body.NewPassword == "" ||
		body.NewPassword == body.CurrentPassword {
		fixture.t.Errorf("student %d password body=%+v", index, body)
	}
	fixture.students[index].newPassword = body.NewPassword
	fixture.setCookies(
		w,
		fmt.Sprintf("student-%d-after", index),
		fmt.Sprintf("student-%d-csrf-after", index),
	)
	fixture.writeUser(
		w,
		fixture.students[index].username,
		fixture.students[index].id,
		"student",
		false,
	)
}

func (fixture *restoreHTTPProbeFixture) serveStudentMe(
	w http.ResponseWriter,
	r *http.Request,
	index int,
) {
	fixture.requireRequest(
		r,
		http.MethodGet,
		"/api/v1/auth/me",
		fmt.Sprintf("student-%d-after", index),
		fmt.Sprintf("student-%d-csrf-after", index),
	)
	fixture.writeUser(
		w,
		fixture.students[index].username,
		fixture.students[index].id,
		"student",
		false,
	)
}

func (fixture *restoreHTTPProbeFixture) serveCreateThread(
	w http.ResponseWriter,
	r *http.Request,
	index int,
) {
	fixture.requireRequest(
		r,
		http.MethodPost,
		"/api/v1/student/questions",
		fmt.Sprintf("student-%d-after", index),
		fmt.Sprintf("student-%d-csrf-after", index),
	)
	key, err := uuid.Parse(r.Header.Get("Idempotency-Key"))
	if err != nil ||
		key == uuid.Nil ||
		key.Version() != 4 ||
		key.Variant() != uuid.RFC4122 ||
		key.String() != r.Header.Get("Idempotency-Key") {
		fixture.t.Errorf("student %d idempotency=%q", index, r.Header.Get("Idempotency-Key"))
	}
	var body struct {
		Title       string `json:"title"`
		Body        string `json:"body"`
		Attachments []any  `json:"attachments"`
	}
	fixture.decodeBody(r, &body)
	if body.Title == "" || body.Body == "" || len(body.Attachments) != 0 {
		fixture.t.Errorf("thread body=%+v", body)
	}
	fixture.writeThreadMutation(w, index)
}

func (fixture *restoreHTTPProbeFixture) serveStudentThread(
	w http.ResponseWriter,
	r *http.Request,
	sessionIndex int,
	threadIndex int,
	status int,
) {
	fixture.requireRequest(
		r,
		http.MethodGet,
		"/api/v1/student/questions/"+fixture.students[threadIndex].threadID,
		fmt.Sprintf("student-%d-after", sessionIndex),
		fmt.Sprintf("student-%d-csrf-after", sessionIndex),
	)
	fixture.writeJSON(w, status, map[string]any{
		"data": map[string]any{
			"thread": fixture.threadJSON(threadIndex, false),
			"messages": []any{
				fixture.messageJSON(threadIndex),
			},
		},
	})
}

func (fixture *restoreHTTPProbeFixture) serveTeacherThread(
	w http.ResponseWriter,
	r *http.Request,
	index int,
) {
	fixture.requireRequest(
		r,
		http.MethodGet,
		"/api/v1/admin/questions/"+fixture.students[index].threadID,
		"teacher-session",
		"teacher-csrf",
	)
	fixture.writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"thread": fixture.threadJSON(index, true),
			"messages": []any{
				fixture.messageJSON(index),
			},
			"notes": []any{},
		},
	})
}

func (fixture *restoreHTTPProbeFixture) serveNotFound(
	w http.ResponseWriter,
	r *http.Request,
	sessionIndex int,
	id string,
	requestID string,
) {
	fixture.requireRequest(
		r,
		http.MethodGet,
		"/api/v1/student/questions/"+id,
		fmt.Sprintf("student-%d-after", sessionIndex),
		fmt.Sprintf("student-%d-csrf-after", sessionIndex),
	)
	if fixture.mode == "bad_404" && fixture.step-1 == 16 {
		fixture.writeError(w, http.StatusNotFound, "forbidden", "different", "")
		return
	}
	fixture.writeError(w, http.StatusNotFound, "not_found", "资源不存在", requestID)
}

func (fixture *restoreHTTPProbeFixture) writeThreadMutation(
	w http.ResponseWriter,
	index int,
) {
	fixture.writeJSON(w, http.StatusCreated, map[string]any{
		"data": map[string]any{
			"thread":  fixture.threadJSON(index, false),
			"message": fixture.messageJSON(index),
		},
	})
}

func (fixture *restoreHTTPProbeFixture) threadJSON(
	index int,
	admin bool,
) map[string]any {
	thread := map[string]any{
		"id":            fixture.students[index].threadID,
		"title":         fmt.Sprintf("Restore probe %d", index),
		"status":        "pending",
		"version":       1,
		"lastMessageAt": "2026-07-29T00:00:00Z",
		"createdAt":     "2026-07-29T00:00:00Z",
		"updatedAt":     "2026-07-29T00:00:00Z",
	}
	if admin {
		thread["studentId"] = fixture.students[index].id
	}
	return thread
}

func (fixture *restoreHTTPProbeFixture) messageJSON(index int) map[string]any {
	return map[string]any{
		"id":          fmt.Sprintf("20000000-0000-4000-8000-00000000000%d", index+1),
		"senderRole":  "student",
		"kind":        "initial",
		"body":        fmt.Sprintf("Restore probe body %d", index),
		"createdAt":   "2026-07-29T00:00:00Z",
		"attachments": []any{},
	}
}

func (fixture *restoreHTTPProbeFixture) requireRequest(
	r *http.Request,
	method string,
	path string,
	session string,
	csrf string,
) {
	if r.Method != method || r.URL.Path != path || r.URL.RawQuery != "" {
		fixture.t.Errorf(
			"request=%s %s?%s want=%s %s",
			r.Method,
			r.URL.Path,
			r.URL.RawQuery,
			method,
			path,
		)
	}
	if session == "" {
		if len(r.Cookies()) != 0 {
			fixture.t.Errorf("unexpected login cookies=%v", r.Cookies())
		}
		if got := r.Header.Values("X-CSRF-Token"); len(got) != 0 {
			fixture.t.Errorf("unexpected login csrf=%q", got)
		}
		return
	}
	sessionCookie, err := r.Cookie("hl_session")
	if err != nil || sessionCookie.Value != session {
		fixture.t.Errorf("session cookie=%v error=%v", sessionCookie, err)
	}
	csrfCookie, err := r.Cookie("hl_csrf")
	if err != nil || csrfCookie.Value != csrf {
		fixture.t.Errorf("csrf cookie=%v error=%v", csrfCookie, err)
	}
	if method == http.MethodPost && r.Header.Get("X-CSRF-Token") != csrf {
		fixture.t.Errorf(
			"csrf header=%q want=%q",
			r.Header.Get("X-CSRF-Token"),
			csrf,
		)
	}
}

func (fixture *restoreHTTPProbeFixture) decodeBody(
	r *http.Request,
	target any,
) {
	if values := r.Header.Values("Content-Type"); len(values) != 1 ||
		values[0] != "application/json" {
		fixture.t.Errorf("content type=%q", values)
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		fixture.t.Errorf("decode body: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		fixture.t.Errorf("trailing request JSON=%v", trailing)
	}
}

func (fixture *restoreHTTPProbeFixture) setCookies(
	w http.ResponseWriter,
	session string,
	csrf string,
) {
	http.SetCookie(w, &http.Cookie{
		Name: "hl_session", Value: session, Path: "/", HttpOnly: true,
	})
	http.SetCookie(w, &http.Cookie{
		Name: "hl_csrf", Value: csrf, Path: "/",
	})
}

func (fixture *restoreHTTPProbeFixture) writeUser(
	w http.ResponseWriter,
	username string,
	id string,
	role string,
	mustChange bool,
) {
	fixture.writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"id":                 id,
			"username":           username,
			"displayName":        "Restore Probe",
			"role":               role,
			"mustChangePassword": mustChange,
		},
	})
}

func (fixture *restoreHTTPProbeFixture) writeError(
	w http.ResponseWriter,
	status int,
	code string,
	message string,
	requestID string,
) {
	fixture.writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":      code,
			"message":   message,
			"requestId": requestID,
		},
	})
}

func (fixture *restoreHTTPProbeFixture) writeJSON(
	w http.ResponseWriter,
	status int,
	value any,
) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		fixture.t.Errorf("encode response: %v", err)
	}
}
