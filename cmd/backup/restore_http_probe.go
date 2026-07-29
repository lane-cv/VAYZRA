package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

const (
	restoreHTTPProbeBaseURL         = "http://app:8080/api/v1"
	restoreHTTPProbeOrigin          = "http://app:8080"
	restoreHTTPProbeCredentialsFile = "/run/secrets/restore-probe-teacher.json"

	restoreHTTPProbeMaxCredentialsBytes  = 4096
	restoreHTTPProbeMaxRequests          = 20
	restoreHTTPProbeMaxRequestBytes      = 16 << 10
	restoreHTTPProbeMaxTotalRequestBytes = 128 << 10
	restoreHTTPProbeMaxResponseBytes     = 64 << 10
	restoreHTTPProbeMaxTotalResponse     = 1 << 20
	restoreHTTPProbeDefaultTotalTimeout  = 30 * time.Second
	restoreHTTPProbeRequestTimeout       = 5 * time.Second
)

var (
	errRestoreHTTPProbeUnavailable = errors.New(
		"restore HTTP probe unavailable",
	)
	restoreHTTPProbeUsernamePattern = regexp.MustCompile(
		`^[a-z0-9._-]{3,64}$`,
	)
)

type restoreHTTPProbeConfig struct {
	baseURL         string
	credentialsFile string
	newUUID         func() (uuid.UUID, error)
	totalTimeout    time.Duration
	transport       http.RoundTripper
}

type restoreHTTPProbeCredentials struct {
	Username string
	Password string
}

type restoreHTTPProbeBudget struct {
	requestCount       int
	totalRequestBytes  int
	totalResponseBytes int
}

type restoreHTTPProbeSession struct {
	baseURL *url.URL
	client  *http.Client
	budget  *restoreHTTPProbeBudget
	userID  uuid.UUID
}

type restoreHTTPProbeUser struct {
	ID                 string `json:"id"`
	Username           string `json:"username"`
	DisplayName        string `json:"displayName"`
	Role               string `json:"role"`
	MustChangePassword bool   `json:"mustChangePassword"`
}

type restoreHTTPProbeStudent struct {
	ID                 string `json:"id"`
	Username           string `json:"username"`
	DisplayName        string `json:"displayName"`
	Status             string `json:"status"`
	MustChangePassword bool   `json:"mustChangePassword"`
	CreatedAt          string `json:"createdAt"`
}

type restoreHTTPProbeThread struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	Status        string  `json:"status"`
	Version       int64   `json:"version"`
	LastMessageAt string  `json:"lastMessageAt"`
	CreatedAt     string  `json:"createdAt"`
	UpdatedAt     string  `json:"updatedAt"`
	CompletedAt   *string `json:"completedAt,omitempty"`
}

type restoreHTTPProbeAdminThread struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	Status        string  `json:"status"`
	Version       int64   `json:"version"`
	LastMessageAt string  `json:"lastMessageAt"`
	CreatedAt     string  `json:"createdAt"`
	UpdatedAt     string  `json:"updatedAt"`
	CompletedAt   *string `json:"completedAt,omitempty"`
	StudentID     string  `json:"studentId"`
}

type restoreHTTPProbeAttachment struct {
	FileVersionID    string `json:"fileVersionId"`
	SortPosition     int    `json:"sortPosition"`
	DisplayName      string `json:"displayName"`
	PreviewAvailable bool   `json:"previewAvailable"`
}

type restoreHTTPProbeMessage struct {
	ID          string                       `json:"id"`
	SenderRole  string                       `json:"senderRole"`
	Kind        string                       `json:"kind"`
	Body        string                       `json:"body"`
	CreatedAt   string                       `json:"createdAt"`
	Attachments []restoreHTTPProbeAttachment `json:"attachments"`
}

type restoreHTTPProbeErrorShape struct {
	Status    int
	Code      string
	Message   string
	RequestID string
}

type restoreHTTPProbeSyntheticStudent struct {
	username          string
	displayName       string
	temporaryPassword string
	newPassword       string
	id                uuid.UUID
	threadID          uuid.UUID
	session           *restoreHTTPProbeSession
}

func runProductionRestoreHTTPProbe(ctx context.Context) error {
	return runRestoreHTTPProbe(ctx, restoreHTTPProbeConfig{
		baseURL:         restoreHTTPProbeBaseURL,
		credentialsFile: restoreHTTPProbeCredentialsFile,
		newUUID:         uuid.NewRandom,
		totalTimeout:    restoreHTTPProbeDefaultTotalTimeout,
	})
}

func runRestoreHTTPProbe(
	ctx context.Context,
	config restoreHTTPProbeConfig,
) error {
	if ctx == nil {
		return errRestoreHTTPProbeUnavailable
	}
	normalized, ok := normalizeRestoreHTTPProbeConfig(config)
	if !ok {
		return errRestoreHTTPProbeUnavailable
	}
	credentials, err := loadRestoreHTTPProbeCredentials(
		normalized.credentialsFile,
	)
	if err != nil {
		return errRestoreHTTPProbeUnavailable
	}
	runContext, cancel := context.WithTimeout(ctx, normalized.totalTimeout)
	defer cancel()

	budget := &restoreHTTPProbeBudget{}
	teacher, err := newRestoreHTTPProbeSession(normalized, budget)
	if err != nil {
		return errRestoreHTTPProbeUnavailable
	}
	if err := teacher.login(
		runContext,
		credentials.Username,
		credentials.Password,
		"admin",
		false,
		uuid.Nil,
	); err != nil {
		return errRestoreHTTPProbeUnavailable
	}
	if err := teacher.me(
		runContext,
		credentials.Username,
		"admin",
		false,
		uuid.Nil,
	); err != nil {
		return errRestoreHTTPProbeUnavailable
	}

	students := make([]restoreHTTPProbeSyntheticStudent, 2)
	for index := range students {
		identity, err := nextRestoreHTTPProbeUUID(normalized.newUUID)
		if err != nil {
			return errRestoreHTTPProbeUnavailable
		}
		suffix := strings.ReplaceAll(identity.String(), "-", "")
		students[index] = restoreHTTPProbeSyntheticStudent{
			username:          "restore-probe-" + suffix,
			displayName:       "Restore Probe " + suffix[:8],
			temporaryPassword: "Restore Probe Temporary " + suffix + "!",
			newPassword:       "Restore Probe Changed " + suffix + "!",
		}
		created, err := teacher.createStudent(runContext, students[index])
		if err != nil {
			return errRestoreHTTPProbeUnavailable
		}
		students[index].id = created.id
	}
	if students[0].username == students[1].username ||
		students[0].id == students[1].id {
		return errRestoreHTTPProbeUnavailable
	}

	for index := range students {
		session, err := newRestoreHTTPProbeSession(normalized, budget)
		if err != nil {
			return errRestoreHTTPProbeUnavailable
		}
		students[index].session = session
		if err := session.login(
			runContext,
			students[index].username,
			students[index].temporaryPassword,
			"student",
			true,
			students[index].id,
		); err != nil {
			return errRestoreHTTPProbeUnavailable
		}
		if err := session.changePassword(
			runContext,
			students[index].temporaryPassword,
			students[index].newPassword,
			students[index].username,
			students[index].id,
		); err != nil {
			return errRestoreHTTPProbeUnavailable
		}
		if err := session.me(
			runContext,
			students[index].username,
			"student",
			false,
			students[index].id,
		); err != nil {
			return errRestoreHTTPProbeUnavailable
		}
		idempotencyKey, err := nextRestoreHTTPProbeUUID(normalized.newUUID)
		if err != nil {
			return errRestoreHTTPProbeUnavailable
		}
		threadID, err := session.createThread(
			runContext,
			students[index].displayName,
			idempotencyKey,
		)
		if err != nil {
			return errRestoreHTTPProbeUnavailable
		}
		students[index].threadID = threadID
		if err := session.getStudentThread(runContext, threadID); err != nil {
			return errRestoreHTTPProbeUnavailable
		}
	}
	if students[0].threadID == students[1].threadID {
		return errRestoreHTTPProbeUnavailable
	}

	for index := range students {
		studentID, err := teacher.getAdminThread(
			runContext,
			students[index].threadID,
		)
		if err != nil || studentID != students[index].id {
			return errRestoreHTTPProbeUnavailable
		}
	}

	missingA, err := nextRestoreHTTPProbeUUID(normalized.newUUID)
	if err != nil {
		return errRestoreHTTPProbeUnavailable
	}
	missingB, err := nextRestoreHTTPProbeUUID(normalized.newUUID)
	if err != nil ||
		missingA == missingB ||
		missingA == students[0].threadID ||
		missingA == students[1].threadID ||
		missingB == students[0].threadID ||
		missingB == students[1].threadID {
		return errRestoreHTTPProbeUnavailable
	}
	notFoundResults := make([]restoreHTTPProbeErrorShape, 0, 4)
	for _, check := range []struct {
		session *restoreHTTPProbeSession
		id      uuid.UUID
	}{
		{session: students[0].session, id: students[1].threadID},
		{session: students[0].session, id: missingA},
		{session: students[1].session, id: students[0].threadID},
		{session: students[1].session, id: missingB},
	} {
		shape, err := check.session.getNotFound(runContext, check.id)
		if err != nil {
			return errRestoreHTTPProbeUnavailable
		}
		notFoundResults = append(notFoundResults, shape)
	}
	expected := notFoundResults[0]
	for _, result := range notFoundResults {
		if result.Status != http.StatusNotFound ||
			result.Code != "not_found" ||
			result.Message == "" ||
			result.RequestID == "" ||
			result.Status != expected.Status ||
			result.Code != expected.Code ||
			result.Message != expected.Message {
			return errRestoreHTTPProbeUnavailable
		}
	}
	if budget.requestCount != restoreHTTPProbeMaxRequests {
		return errRestoreHTTPProbeUnavailable
	}
	return nil
}

func normalizeRestoreHTTPProbeConfig(
	config restoreHTTPProbeConfig,
) (restoreHTTPProbeConfig, bool) {
	if config.baseURL == "" {
		config.baseURL = restoreHTTPProbeBaseURL
	}
	if config.credentialsFile == "" {
		config.credentialsFile = restoreHTTPProbeCredentialsFile
	}
	if config.newUUID == nil {
		config.newUUID = uuid.NewRandom
	}
	if config.totalTimeout == 0 {
		config.totalTimeout = restoreHTTPProbeDefaultTotalTimeout
	}
	parsed, err := url.Parse(config.baseURL)
	if err != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" ||
		parsed.Path != "/api/v1" ||
		parsed.RawPath != "" ||
		!filepath.IsAbs(config.credentialsFile) ||
		config.totalTimeout < time.Millisecond ||
		config.totalTimeout > 2*time.Minute {
		return restoreHTTPProbeConfig{}, false
	}
	config.baseURL = parsed.String()
	return config, true
}

func loadRestoreHTTPProbeCredentials(
	path string,
) (restoreHTTPProbeCredentials, error) {
	if path == "" || !filepath.IsAbs(path) {
		return restoreHTTPProbeCredentials{}, errRestoreHTTPProbeUnavailable
	}
	info, err := os.Lstat(path)
	if err != nil ||
		!safeRestoreHTTPProbeCredentialInfo(info) ||
		info.Size() < 1 ||
		info.Size() > restoreHTTPProbeMaxCredentialsBytes {
		return restoreHTTPProbeCredentials{}, errRestoreHTTPProbeUnavailable
	}
	file, err := openRestoreHTTPProbeCredential(path)
	if err != nil {
		return restoreHTTPProbeCredentials{}, errRestoreHTTPProbeUnavailable
	}
	openedInfo, statErr := file.Stat()
	if statErr != nil ||
		!os.SameFile(info, openedInfo) ||
		!safeRestoreHTTPProbeCredentialInfo(openedInfo) ||
		openedInfo.Size() != info.Size() {
		_ = file.Close()
		return restoreHTTPProbeCredentials{}, errRestoreHTTPProbeUnavailable
	}
	encoded, readErr := io.ReadAll(io.LimitReader(
		file,
		restoreHTTPProbeMaxCredentialsBytes+1,
	))
	closeErr := file.Close()
	if readErr != nil ||
		closeErr != nil ||
		len(encoded) < 1 ||
		len(encoded) > restoreHTTPProbeMaxCredentialsBytes ||
		int64(len(encoded)) != info.Size() {
		return restoreHTTPProbeCredentials{}, errRestoreHTTPProbeUnavailable
	}
	credentials, err := decodeRestoreHTTPProbeCredentials(encoded)
	if err != nil {
		return restoreHTTPProbeCredentials{}, errRestoreHTTPProbeUnavailable
	}
	return credentials, nil
}

func safeRestoreHTTPProbeCredentialInfo(info os.FileInfo) bool {
	if info == nil ||
		!info.Mode().IsRegular() ||
		info.Mode()&os.ModeSymlink != 0 ||
		info.Mode() != 0o400 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}

func openRestoreHTTPProbeCredential(path string) (*os.File, error) {
	descriptor, err := syscall.Open(
		path,
		syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, errRestoreHTTPProbeUnavailable
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = syscall.Close(descriptor)
		return nil, errRestoreHTTPProbeUnavailable
	}
	return file, nil
}

func decodeRestoreHTTPProbeCredentials(
	encoded []byte,
) (restoreHTTPProbeCredentials, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return restoreHTTPProbeCredentials{}, errRestoreHTTPProbeUnavailable
	}
	var credentials restoreHTTPProbeCredentials
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return restoreHTTPProbeCredentials{}, errRestoreHTTPProbeUnavailable
		}
		seen[key] = true
		switch key {
		case "username":
			if err := decoder.Decode(&credentials.Username); err != nil {
				return restoreHTTPProbeCredentials{}, errRestoreHTTPProbeUnavailable
			}
		case "password":
			if err := decoder.Decode(&credentials.Password); err != nil {
				return restoreHTTPProbeCredentials{}, errRestoreHTTPProbeUnavailable
			}
		default:
			return restoreHTTPProbeCredentials{}, errRestoreHTTPProbeUnavailable
		}
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') {
		return restoreHTTPProbeCredentials{}, errRestoreHTTPProbeUnavailable
	}
	if _, err := decoder.Token(); err != io.EOF ||
		!seen["username"] ||
		!seen["password"] ||
		!restoreHTTPProbeUsernamePattern.MatchString(credentials.Username) ||
		auth.ValidatePassword(credentials.Password) != nil {
		return restoreHTTPProbeCredentials{}, errRestoreHTTPProbeUnavailable
	}
	return credentials, nil
}

func newRestoreHTTPProbeSession(
	config restoreHTTPProbeConfig,
	budget *restoreHTTPProbeBudget,
) (*restoreHTTPProbeSession, error) {
	baseURL, err := url.Parse(config.baseURL)
	if err != nil || budget == nil {
		return nil, errRestoreHTTPProbeUnavailable
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, errRestoreHTTPProbeUnavailable
	}
	transport, ok := restoreHTTPProbeTransport(config.transport)
	if !ok {
		return nil, errRestoreHTTPProbeUnavailable
	}
	return &restoreHTTPProbeSession{
		baseURL: baseURL,
		client: &http.Client{
			Transport: transport,
			Jar:       jar,
			Timeout:   restoreHTTPProbeRequestTimeout,
			CheckRedirect: func(
				*http.Request,
				[]*http.Request,
			) error {
				return http.ErrUseLastResponse
			},
		},
		budget: budget,
	}, nil
}

func restoreHTTPProbeTransport(
	injected http.RoundTripper,
) (http.RoundTripper, bool) {
	if injected != nil {
		return injected, true
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok || defaultTransport == nil {
		return nil, false
	}
	transport := defaultTransport.Clone()
	transport.Proxy = nil
	return transport, true
}

func (session *restoreHTTPProbeSession) login(
	ctx context.Context,
	username string,
	password string,
	role string,
	mustChange bool,
	expectedID uuid.UUID,
) error {
	var response struct {
		Data restoreHTTPProbeUser `json:"data"`
	}
	if err := session.doJSON(
		ctx,
		http.MethodPost,
		"/auth/login",
		struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}{Username: username, Password: password},
		"",
		http.StatusOK,
		&response,
	); err != nil ||
		!validRestoreHTTPProbeUser(
			response.Data,
			username,
			role,
			mustChange,
			expectedID,
		) {
		return errRestoreHTTPProbeUnavailable
	}
	actualID, ok := canonicalRestoreHTTPProbeUUID(response.Data.ID)
	if !ok {
		return errRestoreHTTPProbeUnavailable
	}
	session.userID = actualID
	if _, _, ok := session.authenticationCookies(); !ok {
		return errRestoreHTTPProbeUnavailable
	}
	return nil
}

func (session *restoreHTTPProbeSession) me(
	ctx context.Context,
	username string,
	role string,
	mustChange bool,
	expectedID uuid.UUID,
) error {
	if expectedID == uuid.Nil {
		expectedID = session.userID
	}
	if expectedID == uuid.Nil {
		return errRestoreHTTPProbeUnavailable
	}
	var response struct {
		Data restoreHTTPProbeUser `json:"data"`
	}
	if err := session.doJSON(
		ctx,
		http.MethodGet,
		"/auth/me",
		nil,
		"",
		http.StatusOK,
		&response,
	); err != nil ||
		!validRestoreHTTPProbeUser(
			response.Data,
			username,
			role,
			mustChange,
			expectedID,
		) {
		return errRestoreHTTPProbeUnavailable
	}
	return nil
}

func (session *restoreHTTPProbeSession) createStudent(
	ctx context.Context,
	student restoreHTTPProbeSyntheticStudent,
) (restoreHTTPProbeSyntheticStudent, error) {
	var response struct {
		Data restoreHTTPProbeStudent `json:"data"`
	}
	if err := session.doJSON(
		ctx,
		http.MethodPost,
		"/admin/students",
		struct {
			Username          string `json:"username"`
			DisplayName       string `json:"displayName"`
			TemporaryPassword string `json:"temporaryPassword"`
		}{
			Username:          student.username,
			DisplayName:       student.displayName,
			TemporaryPassword: student.temporaryPassword,
		},
		"",
		http.StatusCreated,
		&response,
	); err != nil ||
		response.Data.Username != student.username ||
		response.Data.DisplayName != student.displayName ||
		response.Data.Status != "active" ||
		!response.Data.MustChangePassword ||
		response.Data.CreatedAt == "" {
		return restoreHTTPProbeSyntheticStudent{},
			errRestoreHTTPProbeUnavailable
	}
	id, ok := canonicalRestoreHTTPProbeUUID(response.Data.ID)
	if !ok {
		return restoreHTTPProbeSyntheticStudent{},
			errRestoreHTTPProbeUnavailable
	}
	student.id = id
	return student, nil
}

func (session *restoreHTTPProbeSession) changePassword(
	ctx context.Context,
	currentPassword string,
	newPassword string,
	username string,
	expectedID uuid.UUID,
) error {
	oldSession, oldCSRF, ok := session.authenticationCookies()
	if !ok {
		return errRestoreHTTPProbeUnavailable
	}
	var response struct {
		Data restoreHTTPProbeUser `json:"data"`
	}
	if err := session.doJSON(
		ctx,
		http.MethodPost,
		"/auth/change-password",
		struct {
			CurrentPassword string `json:"currentPassword"`
			NewPassword     string `json:"newPassword"`
		}{
			CurrentPassword: currentPassword,
			NewPassword:     newPassword,
		},
		"",
		http.StatusOK,
		&response,
	); err != nil ||
		!validRestoreHTTPProbeUser(
			response.Data,
			username,
			"student",
			false,
			expectedID,
		) {
		return errRestoreHTTPProbeUnavailable
	}
	newSession, newCSRF, ok := session.authenticationCookies()
	if !ok || newSession == oldSession || newCSRF == oldCSRF {
		return errRestoreHTTPProbeUnavailable
	}
	return nil
}

func (session *restoreHTTPProbeSession) createThread(
	ctx context.Context,
	label string,
	idempotencyKey uuid.UUID,
) (uuid.UUID, error) {
	var response struct {
		Data struct {
			Thread  restoreHTTPProbeThread  `json:"thread"`
			Message restoreHTTPProbeMessage `json:"message"`
		} `json:"data"`
	}
	if err := session.doJSON(
		ctx,
		http.MethodPost,
		"/student/questions",
		struct {
			Title       string                       `json:"title"`
			Body        string                       `json:"body"`
			Attachments []restoreHTTPProbeAttachment `json:"attachments"`
		}{
			Title:       "Restore isolation " + label,
			Body:        "Synthetic restore isolation verification.",
			Attachments: []restoreHTTPProbeAttachment{},
		},
		idempotencyKey.String(),
		http.StatusCreated,
		&response,
	); err != nil {
		return uuid.Nil, errRestoreHTTPProbeUnavailable
	}
	threadID, ok := canonicalRestoreHTTPProbeUUID(response.Data.Thread.ID)
	if !ok || response.Data.Message.ID == "" {
		return uuid.Nil, errRestoreHTTPProbeUnavailable
	}
	return threadID, nil
}

func (session *restoreHTTPProbeSession) getStudentThread(
	ctx context.Context,
	threadID uuid.UUID,
) error {
	var response struct {
		Data struct {
			Thread            restoreHTTPProbeThread    `json:"thread"`
			Messages          []restoreHTTPProbeMessage `json:"messages"`
			NextMessageCursor string                    `json:"nextMessageCursor,omitempty"`
		} `json:"data"`
	}
	if err := session.doJSON(
		ctx,
		http.MethodGet,
		"/student/questions/"+threadID.String(),
		nil,
		"",
		http.StatusOK,
		&response,
	); err != nil {
		return errRestoreHTTPProbeUnavailable
	}
	actual, ok := canonicalRestoreHTTPProbeUUID(response.Data.Thread.ID)
	if !ok || actual != threadID {
		return errRestoreHTTPProbeUnavailable
	}
	return nil
}

func (session *restoreHTTPProbeSession) getAdminThread(
	ctx context.Context,
	threadID uuid.UUID,
) (uuid.UUID, error) {
	var response struct {
		Data struct {
			Thread            restoreHTTPProbeAdminThread `json:"thread"`
			Messages          []restoreHTTPProbeMessage   `json:"messages"`
			Notes             []json.RawMessage           `json:"notes"`
			NextMessageCursor string                      `json:"nextMessageCursor,omitempty"`
		} `json:"data"`
	}
	if err := session.doJSON(
		ctx,
		http.MethodGet,
		"/admin/questions/"+threadID.String(),
		nil,
		"",
		http.StatusOK,
		&response,
	); err != nil {
		return uuid.Nil, errRestoreHTTPProbeUnavailable
	}
	actualThread, ok := canonicalRestoreHTTPProbeUUID(response.Data.Thread.ID)
	if !ok || actualThread != threadID {
		return uuid.Nil, errRestoreHTTPProbeUnavailable
	}
	studentID, ok := canonicalRestoreHTTPProbeUUID(
		response.Data.Thread.StudentID,
	)
	if !ok {
		return uuid.Nil, errRestoreHTTPProbeUnavailable
	}
	return studentID, nil
}

func (session *restoreHTTPProbeSession) getNotFound(
	ctx context.Context,
	threadID uuid.UUID,
) (restoreHTTPProbeErrorShape, error) {
	encoded, status, err := session.request(
		ctx,
		http.MethodGet,
		"/student/questions/"+threadID.String(),
		nil,
		"",
	)
	if err != nil || status != http.StatusNotFound {
		return restoreHTTPProbeErrorShape{},
			errRestoreHTTPProbeUnavailable
	}
	var response struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"requestId"`
		} `json:"error"`
	}
	if decodeRestoreHTTPProbeJSON(encoded, &response) != nil {
		return restoreHTTPProbeErrorShape{},
			errRestoreHTTPProbeUnavailable
	}
	return restoreHTTPProbeErrorShape{
		Status:    status,
		Code:      response.Error.Code,
		Message:   response.Error.Message,
		RequestID: response.Error.RequestID,
	}, nil
}

func (session *restoreHTTPProbeSession) doJSON(
	ctx context.Context,
	method string,
	path string,
	body any,
	idempotencyKey string,
	expectedStatus int,
	response any,
) error {
	encoded, status, err := session.request(
		ctx,
		method,
		path,
		body,
		idempotencyKey,
	)
	if err != nil || status != expectedStatus || response == nil {
		return errRestoreHTTPProbeUnavailable
	}
	if decodeRestoreHTTPProbeJSON(encoded, response) != nil {
		return errRestoreHTTPProbeUnavailable
	}
	return nil
}

func (session *restoreHTTPProbeSession) request(
	ctx context.Context,
	method string,
	path string,
	body any,
	idempotencyKey string,
) ([]byte, int, error) {
	if session == nil ||
		session.baseURL == nil ||
		session.client == nil ||
		session.client.Jar == nil ||
		session.budget == nil ||
		ctx == nil ||
		(method != http.MethodGet && method != http.MethodPost) ||
		!strings.HasPrefix(path, "/") ||
		strings.ContainsAny(path, "?#\r\n") ||
		session.budget.requestCount >= restoreHTTPProbeMaxRequests {
		return nil, 0, errRestoreHTTPProbeUnavailable
	}
	var encodedBody []byte
	var err error
	if body != nil {
		encodedBody, err = json.Marshal(body)
		if err != nil ||
			len(encodedBody) < 1 ||
			len(encodedBody) > restoreHTTPProbeMaxRequestBytes ||
			session.budget.totalRequestBytes >
				restoreHTTPProbeMaxTotalRequestBytes-len(encodedBody) {
			return nil, 0, errRestoreHTTPProbeUnavailable
		}
	}
	requestURL := *session.baseURL
	requestURL.Path += path
	request, err := http.NewRequestWithContext(
		ctx,
		method,
		requestURL.String(),
		bytes.NewReader(encodedBody),
	)
	if err != nil {
		return nil, 0, errRestoreHTTPProbeUnavailable
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if method == http.MethodPost {
		request.Header.Set("Origin", restoreHTTPProbeOrigin)
		if path != "/auth/login" {
			_, csrf, ok := session.authenticationCookies()
			if !ok {
				return nil, 0, errRestoreHTTPProbeUnavailable
			}
			request.Header.Set("X-CSRF-Token", csrf)
		}
	}
	if idempotencyKey != "" {
		id, ok := canonicalRestoreHTTPProbeUUID(idempotencyKey)
		if !ok || id.String() != idempotencyKey {
			return nil, 0, errRestoreHTTPProbeUnavailable
		}
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	session.budget.requestCount++
	session.budget.totalRequestBytes += len(encodedBody)
	response, err := session.client.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, 0, errRestoreHTTPProbeUnavailable
	}
	if response == nil || response.Body == nil {
		return nil, 0, errRestoreHTTPProbeUnavailable
	}
	contentTypes := response.Header.Values("Content-Type")
	mediaType := ""
	if len(contentTypes) == 1 {
		mediaType, _, err = mime.ParseMediaType(contentTypes[0])
	}
	encoded, readErr := io.ReadAll(io.LimitReader(
		response.Body,
		restoreHTTPProbeMaxResponseBytes+1,
	))
	closeErr := response.Body.Close()
	if err != nil ||
		len(contentTypes) != 1 ||
		mediaType != "application/json" ||
		readErr != nil ||
		closeErr != nil ||
		len(encoded) < 1 ||
		len(encoded) > restoreHTTPProbeMaxResponseBytes ||
		session.budget.totalResponseBytes >
			restoreHTTPProbeMaxTotalResponse-len(encoded) {
		return nil, 0, errRestoreHTTPProbeUnavailable
	}
	session.budget.totalResponseBytes += len(encoded)
	return encoded, response.StatusCode, nil
}

func (session *restoreHTTPProbeSession) authenticationCookies() (
	string,
	string,
	bool,
) {
	if session == nil ||
		session.client == nil ||
		session.client.Jar == nil ||
		session.baseURL == nil {
		return "", "", false
	}
	var sessionValue, csrfValue string
	sessionCount := 0
	csrfCount := 0
	for _, cookie := range session.client.Jar.Cookies(session.baseURL) {
		switch cookie.Name {
		case "hl_session":
			sessionCount++
			sessionValue = cookie.Value
		case "hl_csrf":
			csrfCount++
			csrfValue = cookie.Value
		}
	}
	return sessionValue, csrfValue,
		sessionCount == 1 &&
			csrfCount == 1 &&
			sessionValue != "" &&
			csrfValue != ""
}

func decodeRestoreHTTPProbeJSON(encoded []byte, target any) error {
	if len(encoded) < 1 || target == nil {
		return errRestoreHTTPProbeUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errRestoreHTTPProbeUnavailable
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errRestoreHTTPProbeUnavailable
	}
	return nil
}

func validRestoreHTTPProbeUser(
	user restoreHTTPProbeUser,
	username string,
	role string,
	mustChange bool,
	expectedID uuid.UUID,
) bool {
	id, ok := canonicalRestoreHTTPProbeUUID(user.ID)
	if !ok ||
		user.Username != username ||
		user.DisplayName == "" ||
		user.Role != role ||
		user.MustChangePassword != mustChange {
		return false
	}
	return expectedID == uuid.Nil || id == expectedID
}

func nextRestoreHTTPProbeUUID(
	newUUID func() (uuid.UUID, error),
) (uuid.UUID, error) {
	if newUUID == nil {
		return uuid.Nil, errRestoreHTTPProbeUnavailable
	}
	id, err := newUUID()
	if err != nil ||
		id == uuid.Nil ||
		id.Version() != 4 ||
		id.Variant() != uuid.RFC4122 {
		return uuid.Nil, errRestoreHTTPProbeUnavailable
	}
	return id, nil
}

func canonicalRestoreHTTPProbeUUID(raw string) (uuid.UUID, bool) {
	id, err := uuid.Parse(raw)
	return id, err == nil &&
		id != uuid.Nil &&
		id.Version() == 4 &&
		id.Variant() == uuid.RFC4122 &&
		id.String() == raw
}
