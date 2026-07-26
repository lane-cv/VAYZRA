package aiqa

import (
	"errors"
	"testing"
	"time"
)

func TestQuotaEffectiveLimitsInheritanceAndDisable(t *testing.T) {
	global := QuotaLimits{DailyRequests: 10, MonthlyRequests: 20, DailyTokens: 30, MonthlyTokens: 40}
	if got, err := ResolveQuotaLimits(global, StudentQuotaLimits{DailyRequests: int64ptr(2)}); err != nil || got.DailyRequests != 2 || got.MonthlyTokens != 40 {
		t.Fatalf("inheritance: got=%+v err=%v", got, err)
	}
	zero := int64(0)
	if _, err := ResolveQuotaLimits(global, StudentQuotaLimits{MonthlyTokens: &zero}); !errors.Is(err, ErrAIDisabled) {
		t.Fatalf("zero override must disable AI, got %v", err)
	}
	global.DailyRequests = 0
	if _, err := ResolveQuotaLimits(global, StudentQuotaLimits{}); !errors.Is(err, ErrAIDisabled) {
		t.Fatalf("zero global limit must disable AI, got %v", err)
	}
}

func TestQuotaShanghaiKeysAtBoundaries(t *testing.T) {
	before := time.Date(2026, 7, 31, 15, 59, 59, 0, time.UTC)
	after := before.Add(time.Second)
	if day, month := ShanghaiQuotaKeys(before); day != "2026-07-31" || month != "2026-07" {
		t.Fatalf("before boundary: %s %s", day, month)
	}
	if day, month := ShanghaiQuotaKeys(after); day != "2026-08-01" || month != "2026-08" {
		t.Fatalf("after boundary: %s %s", day, month)
	}
}

func TestQuotaReservationIsConservativeUTF8ImagesAndOutput(t *testing.T) {
	got := EstimateQuotaReservation("系统", []string{"a", "题"}, "文档", 2, 50, 100, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	want := int64(len([]byte("系统a题文档")) + 2*50 + 100)
	if got.TokenCount != want || got.RequestCount != 1 || got.EstimatorVersion != CurrentEstimatorVersion {
		t.Fatalf("got %+v want tokens %d", got, want)
	}
}

func TestQuotaAdmissionAllowsExactRemainder(t *testing.T) {
	limits := QuotaLimits{DailyRequests: 2, MonthlyRequests: 3, DailyTokens: 11, MonthlyTokens: 12}
	used := QuotaUsage{DailyRequests: 1, MonthlyRequests: 2, DailyTokens: 6, MonthlyTokens: 7}
	res := QuotaReservation{RequestCount: 1, TokenCount: 5}
	if err := CheckQuota(limits, used, res); err != nil {
		t.Fatalf("exact remainder rejected: %v", err)
	}
	res.TokenCount++
	if err := CheckQuota(limits, used, res); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("over remainder must fail, got %v", err)
	}
}

func int64ptr(v int64) *int64 { return &v }
