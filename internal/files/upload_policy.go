package files

import (
	"path/filepath"
	"strings"

	"happylearn.local/app/internal/auth"
)

type UploadPolicy interface {
	Purpose() UploadPurpose
	Authorize(auth.User) error
	Validate(CreateUploadInput) error
}

type TeachingUploadPolicy struct{}

func (TeachingUploadPolicy) Purpose() UploadPurpose { return UploadPurposeTeaching }
func (TeachingUploadPolicy) Authorize(user auth.User) error {
	if user.ID == [16]byte{} || user.Role != auth.RoleAdmin || user.Status != auth.StatusActive {
		return ErrForbidden
	}
	return nil
}
func (TeachingUploadPolicy) Validate(in CreateUploadInput) error {
	if !validCreateInput(in) {
		return ErrInvalid
	}
	return nil
}

type QAUploadPolicy struct{}

func (QAUploadPolicy) Purpose() UploadPurpose { return UploadPurposeQA }
func (QAUploadPolicy) Authorize(user auth.User) error {
	if user.ID == [16]byte{} || user.Status != auth.StatusActive || (user.Role != auth.RoleStudent && user.Role != auth.RoleAdmin) {
		return ErrForbidden
	}
	return nil
}
func (QAUploadPolicy) Validate(in CreateUploadInput) error {
	return validateLimitedUpload(in, qaUploadLimits)
}

type AIUploadPolicy struct{}

func (AIUploadPolicy) Purpose() UploadPurpose { return UploadPurposeAI }
func (AIUploadPolicy) Authorize(user auth.User) error {
	if user.ID == [16]byte{} || user.Status != auth.StatusActive || user.Role != auth.RoleStudent {
		return ErrForbidden
	}
	return nil
}
func (AIUploadPolicy) Validate(in CreateUploadInput) error {
	return validateLimitedUpload(in, aiUploadLimits)
}

func validateLimitedUpload(in CreateUploadInput, limits map[string]map[string]int64) error {
	if in.ExpectedSize < 1 || !validHash(in.ExpectedSHA256) || !validDisplayName(in.DisplayName) {
		return ErrInvalid
	}
	ext := strings.ToLower(filepath.Ext(in.DisplayName))
	limit, ok := limits[ext][in.DeclaredMIME]
	if !ok {
		return ErrFileTypeRejected
	}
	if in.ExpectedSize > limit {
		return ErrFileTooLarge
	}
	return nil
}

var qaUploadLimits = map[string]map[string]int64{
	".jpg": {"image/jpeg": 20 << 20}, ".jpeg": {"image/jpeg": 20 << 20},
	".png": {"image/png": 20 << 20}, ".webp": {"image/webp": 20 << 20}, ".gif": {"image/gif": 20 << 20},
	".pdf":  {"application/pdf": 50 << 20},
	".docx": {"application/vnd.openxmlformats-officedocument.wordprocessingml.document": 30 << 20},
	".xlsx": {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": 30 << 20},
	".pptx": {"application/vnd.openxmlformats-officedocument.presentationml.presentation": 30 << 20},
	".txt":  {"text/plain": 10 << 20}, ".md": {"text/markdown": 10 << 20},
}

var aiUploadLimits = map[string]map[string]int64{
	".jpg": {"image/jpeg": 20 << 20}, ".jpeg": {"image/jpeg": 20 << 20},
	".png": {"image/png": 20 << 20}, ".webp": {"image/webp": 20 << 20}, ".gif": {"image/gif": 20 << 20},
	".pdf":  {"application/pdf": 50 << 20},
	".docx": {"application/vnd.openxmlformats-officedocument.wordprocessingml.document": 30 << 20},
	".txt":  {"text/plain": 10 << 20}, ".md": {"text/markdown": 10 << 20},
}
