package files

import "context"

type cleanupAdmissionContextKey struct{}

type cleanupAdmissionContextValue struct {
	admitted bool
}

var admittedCleanupContext = &cleanupAdmissionContextValue{admitted: true}

func withCleanupAdmission(ctx context.Context) context.Context {
	return context.WithValue(
		ctx,
		cleanupAdmissionContextKey{},
		admittedCleanupContext,
	)
}

func hasCleanupAdmission(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	return ctx.Value(cleanupAdmissionContextKey{}) == admittedCleanupContext
}
