package aiqa

import (
	"context"
	"errors"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/operations"
)

var (
	ErrNoRunnableRun   = errors.New("no runnable AI run")
	ErrRunnerLeaseLost = errors.New("AI runner lease lost")
	ErrCancelRequested = errors.New("AI run cancellation requested")
)

type RunnerStore interface {
	LeaseNext(context.Context, string, time.Time, time.Duration) (LeasedRun, error)
	Heartbeat(context.Context, uuid.UUID, string, time.Time) error
	AppendEvents(context.Context, uuid.UUID, string, []RunEvent) error
	Complete(context.Context, LeasedRun, Completion) error
	Fail(context.Context, LeasedRun, Failure) error
	ReconcileExpired(context.Context, time.Time, int) error
}

type LeasedRun struct {
	Run        Run
	Config     RuntimeProviderConfig
	Request    GatewayRequest
	LeaseOwner string
}

type RunEvent struct {
	Sequence               int64
	Kind, Delta, ErrorCode string
	CreatedAt              time.Time
}

type Completion struct {
	Answer                                  string
	InputTokens, OutputTokens, CostMicroUSD int64
	UsageSource, FinishReason               string
	FirstByteMS, TotalMS                    int64
}

type Failure struct {
	Status                                  RunStatus
	ErrorCode, UsageSource                  string
	InputTokens, OutputTokens, CostMicroUSD int64
	TotalMS                                 int64
}

type Runner struct {
	Store             RunnerStore
	Gateway           Gateway
	Owner             string
	GlobalConcurrency int
	PollInterval      time.Duration
	LeaseDuration     time.Duration
	FlushInterval     time.Duration
	FlushBytes        int
	ClaimGate         operations.ClaimGate
	LogCategory       func(string)
}

func StartRunner(r Runner) func() {
	if r.GlobalConcurrency <= 0 {
		r.GlobalConcurrency = 1
	}
	if r.PollInterval <= 0 {
		r.PollInterval = time.Second
	}
	if r.LeaseDuration <= 0 {
		r.LeaseDuration = 30 * time.Second
	}
	if r.FlushInterval <= 0 {
		r.FlushInterval = 250 * time.Millisecond
	}
	if r.FlushBytes <= 0 {
		r.FlushBytes = 4 << 10
	}
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	if r.Store != nil && r.Gateway != nil && r.Owner != "" {
		workers.Add(r.GlobalConcurrency + 1)
		for i := 0; i < r.GlobalConcurrency; i++ {
			go func() {
				defer workers.Done()
				r.worker(ctx)
			}()
		}
		go func() {
			defer workers.Done()
			r.reconciler(ctx)
		}()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			workers.Wait()
		})
	}
}

func (r Runner) worker(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if r.ClaimGate != nil {
			allowed, gateErr := r.ClaimGate.ClaimsAllowed(ctx)
			switch {
			case gateErr != nil:
				r.log("operational_gate_failed")
				timer.Reset(r.PollInterval)
				continue
			case !allowed:
				timer.Reset(r.PollInterval)
				continue
			}
		}
		leased, err := r.Store.LeaseNext(ctx, r.Owner, time.Now().UTC(), r.LeaseDuration)
		switch {
		case err == nil:
			r.execute(ctx, leased)
			timer.Reset(0)
		case errors.Is(err, ErrNoRunnableRun):
			timer.Reset(r.PollInterval)
		case ctx.Err() != nil:
			return
		default:
			timer.Reset(r.PollInterval)
		}
	}
}

func (r Runner) log(category string) {
	if r.LogCategory != nil {
		r.LogCategory(category)
		return
	}
	log.Printf("ai_runner category=%s", category)
}

func (r Runner) reconciler(ctx context.Context) {
	_ = r.Store.ReconcileExpired(ctx, time.Now().UTC(), r.GlobalConcurrency*4)
	ticker := time.NewTicker(r.LeaseDuration)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = r.Store.ReconcileExpired(ctx, time.Now().UTC(), r.GlobalConcurrency*4)
		}
	}
}

type runBuffer struct {
	mu           sync.Mutex
	events       []RunEvent
	bytes        int
	answer       strings.Builder
	nextSequence int64
	inputTokens  int64
	outputTokens int64
	finishReason string
	usageSeen    bool
	firstByteAt  time.Time
	storeErr     error
}

func (r Runner) execute(parent context.Context, leased LeasedRun) {
	defer zeroBytes(leased.Config.APIKey)
	started := time.Now()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	buffer := &runBuffer{nextSequence: leased.Run.LastSequence + 1}
	backgroundDone := make(chan struct{})
	var background sync.WaitGroup
	background.Add(2)
	go func() {
		defer background.Done()
		ticker := time.NewTicker(r.FlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-backgroundDone:
				return
			case <-ticker.C:
				if err := r.flush(ctx, leased, buffer); err != nil {
					cancel()
				}
			}
		}
	}()
	go func() {
		defer background.Done()
		interval := r.LeaseDuration / 3
		if interval <= 0 {
			interval = time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		err := heartbeatOnTicks(ctx, backgroundDone, ticker.C, time.Now, r.LeaseDuration, func(leaseUntil time.Time) error {
			return r.Store.Heartbeat(ctx, leased.Run.ID, leased.LeaseOwner, leaseUntil)
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			buffer.mu.Lock()
			if buffer.storeErr == nil {
				buffer.storeErr = err
			}
			buffer.mu.Unlock()
			cancel()
		}
	}()

	streamErr := r.Gateway.Stream(ctx, leased.Config, leased.Request, func(event GatewayEvent) error {
		now := time.Now()
		buffer.mu.Lock()
		if buffer.storeErr != nil {
			err := buffer.storeErr
			buffer.mu.Unlock()
			return err
		}
		switch event.Kind {
		case "delta":
			if event.Delta == "" {
				buffer.mu.Unlock()
				return nil
			}
			if buffer.firstByteAt.IsZero() {
				buffer.firstByteAt = now
			}
			buffer.answer.WriteString(event.Delta)
		case "usage":
			buffer.inputTokens = event.InputTokens
			buffer.outputTokens = event.OutputTokens
			buffer.usageSeen = true
		case "completed":
			buffer.finishReason = event.FinishReason
			buffer.mu.Unlock()
			return nil
		default:
			buffer.mu.Unlock()
			return nil
		}
		buffer.events = append(buffer.events, RunEvent{
			Sequence: buffer.nextSequence, Kind: event.Kind, Delta: event.Delta, CreatedAt: now.UTC(),
		})
		buffer.nextSequence++
		buffer.bytes += len(event.Delta)
		flush := buffer.bytes >= r.FlushBytes
		buffer.mu.Unlock()
		if flush {
			return r.flush(ctx, leased, buffer)
		}
		return nil
	})
	close(backgroundDone)
	background.Wait()
	durableCtx, durableCancel := context.WithTimeout(context.WithoutCancel(parent), runnerTerminalTimeout(r.LeaseDuration))
	defer durableCancel()
	if err := r.flush(durableCtx, leased, buffer); err != nil && streamErr == nil {
		streamErr = err
	}

	buffer.mu.Lock()
	storeErr := buffer.storeErr
	answer := buffer.answer.String()
	input, output := buffer.inputTokens, buffer.outputTokens
	finish, usageSeen := buffer.finishReason, buffer.usageSeen
	firstByte := buffer.firstByteAt
	buffer.mu.Unlock()
	totalMS := max(time.Since(started).Milliseconds(), 0)

	if parent.Err() != nil {
		return
	}
	if storeErr != nil {
		switch {
		case errors.Is(storeErr, ErrCancelRequested):
			_ = r.Store.Fail(durableCtx, leased, Failure{Status: RunCancelled, ErrorCode: "cancelled", UsageSource: "unknown", TotalMS: totalMS})
		case errors.Is(storeErr, ErrRunnerLeaseLost):
			_ = r.Store.Fail(durableCtx, leased, Failure{Status: RunFailed, ErrorCode: "lease_lost", UsageSource: "unknown", TotalMS: totalMS})
		default:
			_ = r.Store.Fail(durableCtx, leased, Failure{Status: RunFailed, ErrorCode: "callback_store_failure", UsageSource: "unknown", TotalMS: totalMS})
		}
		return
	}
	if streamErr != nil {
		_ = r.Store.Fail(durableCtx, leased, Failure{
			Status: RunFailed, ErrorCode: stableGatewayCategory(streamErr), UsageSource: "unknown", TotalMS: totalMS,
		})
		return
	}
	usageSource := "upstream"
	if !usageSeen {
		usageSource = "estimated"
		input = estimateGatewayInput(leased.Request, leased.Config.Model.ImageQuotaTokens)
		output = int64(len([]byte(answer)))
	}
	firstByteMS := totalMS
	if !firstByte.IsZero() {
		firstByteMS = max(firstByte.Sub(started).Milliseconds(), 0)
	}
	inputCost := tokenCost(input, leased.Config.Model.InputPriceMicroUSD)
	outputCost := tokenCost(output, leased.Config.Model.OutputPriceMicroUSD)
	cost := inputCost
	if outputCost > math.MaxInt64-cost {
		cost = math.MaxInt64
	} else {
		cost += outputCost
	}
	err := r.Store.Complete(durableCtx, leased, Completion{
		Answer: answer, InputTokens: input, OutputTokens: output, CostMicroUSD: cost,
		UsageSource: usageSource, FinishReason: finish, FirstByteMS: firstByteMS, TotalMS: totalMS,
	})
	if errors.Is(err, ErrCancelRequested) {
		_ = r.Store.Fail(durableCtx, leased, Failure{
			Status: RunCancelled, ErrorCode: "cancelled", UsageSource: "unknown", TotalMS: totalMS,
		})
	} else if err != nil {
		_ = r.Store.Fail(durableCtx, leased, Failure{
			Status: RunFailed, ErrorCode: "terminal_store_failure", UsageSource: "unknown", TotalMS: totalMS,
		})
	}
}

func estimateGatewayInput(request GatewayRequest, imageQuotaTokens int64) int64 {
	total := int64(len([]byte(request.SystemPrompt)))
	for _, turn := range request.Turns {
		textBytes := int64(len([]byte(turn.Text)))
		if textBytes > math.MaxInt64-total {
			return math.MaxInt64
		}
		total += textBytes
	}
	if imageQuotaTokens <= 0 || len(request.Images) == 0 {
		return total
	}
	images := int64(len(request.Images))
	if images > (math.MaxInt64-total)/imageQuotaTokens {
		return math.MaxInt64
	}
	return total + images*imageQuotaTokens
}

func (r Runner) flush(ctx context.Context, leased LeasedRun, buffer *runBuffer) error {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.storeErr != nil {
		return buffer.storeErr
	}
	if len(buffer.events) == 0 {
		return nil
	}
	events := append([]RunEvent(nil), buffer.events...)
	if err := r.Store.AppendEvents(ctx, leased.Run.ID, leased.LeaseOwner, events); err != nil {
		buffer.storeErr = err
		return err
	}
	buffer.events = buffer.events[:0]
	buffer.bytes = 0
	return nil
}

func stableGatewayCategory(err error) string {
	var gatewayErr *GatewayError
	if errors.As(err, &gatewayErr) {
		switch gatewayErr.Category {
		case "auth", "rate_limited", "upstream_4xx", "upstream_5xx", "malformed_stream",
			"response_too_large", "stream_interrupted", "timeout", "cancelled":
			return gatewayErr.Category
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout_total"
	}
	return "stream_interrupted"
}

func tokenCost(tokens, price int64) int64 {
	if tokens <= 0 || price <= 0 {
		return 0
	}
	const million int64 = 1000000
	wholeTokens, remainingTokens := tokens/million, tokens%million
	if wholeTokens > math.MaxInt64/price {
		return math.MaxInt64
	}
	cost := wholeTokens * price
	priceMillions, priceRemainder := price/million, price%million
	parts := []int64{
		remainingTokens * priceMillions,
		(remainingTokens*priceRemainder + million - 1) / million,
	}
	for _, part := range parts {
		if part > math.MaxInt64-cost {
			return math.MaxInt64
		}
		cost += part
	}
	return cost
}

func runnerTerminalTimeout(leaseDuration time.Duration) time.Duration {
	timeout := leaseDuration / 3
	if timeout <= 0 || timeout > 2*time.Second {
		return 2 * time.Second
	}
	return timeout
}
