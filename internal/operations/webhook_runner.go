package operations

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	DefaultWebhookPollInterval = 5 * time.Second
	defaultWebhookSendTimeout  = 15 * time.Second
	defaultWebhookStoreTimeout = 2 * time.Second
)

type WebhookDeliveryStore interface {
	Claim(
		context.Context,
		string,
		uuid.UUID,
		time.Time,
		time.Duration,
	) (*WebhookDeliveryJob, error)
	Complete(
		context.Context,
		WebhookDeliveryJob,
		WebhookDeliveryResult,
		time.Time,
	) error
	Abandon(context.Context, WebhookDeliveryJob) error
}

type WebhookDeliverySender interface {
	Send(context.Context, WebhookPayload) WebhookDeliveryResult
}

type WebhookDeliveryRunner struct {
	Store           WebhookDeliveryStore
	Sender          WebhookDeliverySender
	Clock           func() time.Time
	NewUUID         func() uuid.UUID
	ClaimOwner      string
	PollInterval    time.Duration
	LeaseDuration   time.Duration
	DeliveryTimeout time.Duration
	LogCategory     func(string)
}

func (runner WebhookDeliveryRunner) RunOnce(ctx context.Context) error {
	runner, err := runner.normalized()
	if err != nil || ctx == nil {
		return ErrInvalid
	}
	now := runner.Clock().UTC()
	token := runner.NewUUID()
	if !validSampleTime(now) || token == uuid.Nil {
		return ErrInvalid
	}
	job, err := runner.Store.Claim(
		ctx,
		runner.ClaimOwner,
		token,
		now,
		runner.LeaseDuration,
	)
	if err != nil || job == nil {
		return err
	}
	if ctx.Err() != nil {
		return runner.abandon(ctx.Err(), *job)
	}
	payload, err := BuildWebhookPayload(job.Event)
	if err != nil {
		return runner.complete(
			ctx,
			*job,
			webhookProtocolFailure(),
		)
	}
	deliveryCtx, cancel := context.WithTimeout(ctx, runner.DeliveryTimeout)
	result := runner.Sender.Send(deliveryCtx, payload)
	cancel()
	if ctx.Err() != nil {
		return runner.abandon(ctx.Err(), *job)
	}
	if !validWebhookDeliveryResult(result) {
		result = webhookProtocolFailure()
	}
	return runner.complete(ctx, *job, result)
}

func (runner WebhookDeliveryRunner) complete(
	ctx context.Context,
	job WebhookDeliveryJob,
	result WebhookDeliveryResult,
) error {
	finishedAt := runner.Clock().UTC()
	if !validSampleTime(finishedAt) ||
		finishedAt.Before(job.StartedAt) {
		return runner.abandon(ErrInvalid, job)
	}
	return runner.Store.Complete(ctx, job, result, finishedAt)
}

func (runner WebhookDeliveryRunner) abandon(
	cause error,
	job WebhookDeliveryJob,
) error {
	abandonCtx, cancel := context.WithTimeout(
		context.Background(),
		defaultWebhookStoreTimeout,
	)
	defer cancel()
	return errors.Join(cause, runner.Store.Abandon(abandonCtx, job))
}

func (runner WebhookDeliveryRunner) normalized() (WebhookDeliveryRunner, error) {
	if runner.Store == nil ||
		runner.Sender == nil ||
		runner.Clock == nil ||
		runner.NewUUID == nil ||
		!webhookClaimOwner.MatchString(runner.ClaimOwner) {
		return WebhookDeliveryRunner{}, ErrInvalid
	}
	if runner.PollInterval <= 0 {
		runner.PollInterval = DefaultWebhookPollInterval
	}
	if runner.LeaseDuration <= 0 {
		runner.LeaseDuration = DefaultWebhookLeaseDuration
	}
	if runner.DeliveryTimeout <= 0 {
		runner.DeliveryTimeout = defaultWebhookSendTimeout
	}
	if runner.PollInterval <= 0 ||
		runner.LeaseDuration <= 0 ||
		runner.LeaseDuration > maxWebhookLeaseDuration ||
		runner.DeliveryTimeout <= 0 ||
		runner.DeliveryTimeout >= runner.LeaseDuration ||
		runner.DeliveryTimeout > maxWebhookNetworkTimeout {
		return WebhookDeliveryRunner{}, ErrInvalid
	}
	return runner, nil
}

func StartWebhookDeliveryRunner(
	runner WebhookDeliveryRunner,
) (func(), error) {
	runner, err := runner.normalized()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				err := runner.RunOnce(ctx)
				if err != nil && !errors.Is(err, context.Canceled) {
					runner.log("delivery_failed")
				}
				timer.Reset(runner.PollInterval)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}, nil
}

func (runner WebhookDeliveryRunner) log(category string) {
	if runner.LogCategory != nil {
		runner.LogCategory(category)
		return
	}
	log.Printf("alert_webhook category=%s", category)
}
