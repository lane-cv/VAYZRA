package aiqa

import (
	"errors"
	"time"
)

const CurrentEstimatorVersion int16 = 1

var (
	ErrQuotaExceeded   = errors.New("ai quota exceeded")
	ErrAIBusy          = errors.New("ai run already active")
	ErrContextTooLarge = errors.New("ai context too large")
	ErrRunConflict     = errors.New("ai run conflict")
)

type QuotaLimits struct {
	DailyRequests, MonthlyRequests, DailyTokens, MonthlyTokens int64
}

type StudentQuotaLimits struct {
	DailyRequests, MonthlyRequests, DailyTokens, MonthlyTokens *int64
}

type QuotaUsage struct {
	DailyRequests, MonthlyRequests, DailyTokens, MonthlyTokens int64
}

type QuotaReservation struct {
	RequestCount     int64
	TokenCount       int64
	DayKey           string
	MonthKey         string
	EstimatorVersion int16
}

func ResolveQuotaLimits(global QuotaLimits, student StudentQuotaLimits) (QuotaLimits, error) {
	out := global
	if student.DailyRequests != nil {
		out.DailyRequests = *student.DailyRequests
	}
	if student.MonthlyRequests != nil {
		out.MonthlyRequests = *student.MonthlyRequests
	}
	if student.DailyTokens != nil {
		out.DailyTokens = *student.DailyTokens
	}
	if student.MonthlyTokens != nil {
		out.MonthlyTokens = *student.MonthlyTokens
	}
	if out.DailyRequests <= 0 || out.MonthlyRequests <= 0 || out.DailyTokens <= 0 || out.MonthlyTokens <= 0 {
		return QuotaLimits{}, ErrAIDisabled
	}
	return out, nil
}

func ShanghaiQuotaKeys(now time.Time) (string, string) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	local := now.In(loc)
	return local.Format("2006-01-02"), local.Format("2006-01")
}

func EstimateQuotaReservation(systemPrompt string, selectedTextTurns []string, extractedText string, imageCount int, imageQuotaTokens, maxOutputTokens int64, now time.Time) QuotaReservation {
	n := int64(len([]byte(systemPrompt)) + len([]byte(extractedText)))
	for _, turn := range selectedTextTurns {
		n += int64(len([]byte(turn)))
	}
	n += int64(imageCount)*imageQuotaTokens + maxOutputTokens
	day, month := ShanghaiQuotaKeys(now)
	return QuotaReservation{RequestCount: 1, TokenCount: n, DayKey: day, MonthKey: month, EstimatorVersion: CurrentEstimatorVersion}
}

func CheckQuota(limits QuotaLimits, used QuotaUsage, reservation QuotaReservation) error {
	if reservation.RequestCount < 1 || reservation.TokenCount < 0 ||
		used.DailyRequests+reservation.RequestCount > limits.DailyRequests ||
		used.MonthlyRequests+reservation.RequestCount > limits.MonthlyRequests ||
		used.DailyTokens+reservation.TokenCount > limits.DailyTokens ||
		used.MonthlyTokens+reservation.TokenCount > limits.MonthlyTokens {
		return ErrQuotaExceeded
	}
	return nil
}
