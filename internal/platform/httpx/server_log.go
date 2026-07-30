package httpx

import (
	"log"

	"happylearn.local/app/internal/platform/safelog"
)

// SafeServerErrorLog replaces net/http's raw default logger. The writer never
// records the message bytes; it emits only a fixed structured event.
func SafeServerErrorLog(logger safelog.Logger, service string) *log.Logger {
	return log.New(safeServerLogWriter{
		logger:  logger,
		service: service,
	}, "", 0)
}

type safeServerLogWriter struct {
	logger  safelog.Logger
	service string
}

func (writer safeServerLogWriter) Write(message []byte) (int, error) {
	writer.logger.Error(
		"http.server.error",
		safelog.Field{Name: "category", Value: "runtime"},
		safelog.Field{Name: "service", Value: writer.service},
	)
	return len(message), nil
}
