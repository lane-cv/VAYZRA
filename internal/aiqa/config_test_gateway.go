package aiqa

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

type ConnectivityTester interface {
	Test(context.Context, RuntimeProviderConfig) (ConnectivityResult, error)
}

type providerConnectivityTester struct {
	policy URLPolicy
	now    func() time.Time
}

func NewProviderConnectivityTester(policy URLPolicy) ConnectivityTester {
	return &providerConnectivityTester{policy: policy, now: time.Now}
}

func (t *providerConnectivityTester) Test(ctx context.Context, cfg RuntimeProviderConfig) (result ConnectivityResult, err error) {
	result.Protocol = cfg.ProtocolMode
	started := t.now()
	defer func() {
		result.LatencyMS = t.now().Sub(started).Milliseconds()
		zeroBytes(cfg.APIKey)
	}()

	gateway := NewGateway(NewSafeHTTPClient(t.policy, cfg.Timeouts))
	err = gateway.Stream(ctx, cfg, GatewayRequest{
		RunID:           uuid.New(),
		Model:           cfg.Model.UpstreamModelID,
		Turns:           []GatewayTurn{{Role: "student", Text: "x"}},
		MaxOutputTokens: 1,
	}, func(GatewayEvent) error {
		return nil
	})
	if err == nil {
		result.OK = true
		return result, nil
	}
	var gatewayFailure *GatewayError
	if errors.As(err, &gatewayFailure) {
		result.ErrorCategory = gatewayFailure.Category
	} else {
		result.ErrorCategory = "unavailable"
	}
	return result, err
}
