package main

import (
	"context"
	"time"

	"happylearn.local/app/internal/operations"
	"happylearn.local/app/internal/platform/config"
)

func recordOwnedInfrastructureStatuses(
	ctx context.Context,
	writer operations.InfrastructureStatusWriter,
	cfg config.Config,
	webhookEnabled bool,
	validatedAt time.Time,
) error {
	statuses := []struct {
		key        operations.InfrastructureKey
		configured bool
	}{
		{operations.InfrastructureApplicationDatabase, cfg.DatabaseURL != ""},
		{
			operations.InfrastructureRedisSecurity,
			cfg.RedisURL != "" && cfg.LoginThrottleSecret != "",
		},
		{
			operations.InfrastructureObjectStore,
			cfg.MinIOEndpoint != "" &&
				cfg.MinIOAccessKey != "" &&
				cfg.MinIOSecretKey != "" &&
				cfg.MinIOOriginalsBucket != "" &&
				cfg.MinIOPreviewsBucket != "",
		},
		{operations.InfrastructureAIEncryption, len(cfg.AIMasterKey) == 32 && cfg.AIMasterKeyVersion > 0},
		{operations.InfrastructureInternalMetrics, cfg.MetricsBearerSecret != ""},
		{operations.InfrastructureHostMetricsIngestion, len(cfg.HostMetricsHMACSecret) != 0},
		{operations.InfrastructureAlertWebhook, webhookEnabled},
	}
	for _, status := range statuses {
		if err := writer.RecordInfrastructureStatus(
			ctx,
			status.key,
			status.configured,
			validatedAt,
		); err != nil {
			return err
		}
	}
	return nil
}
