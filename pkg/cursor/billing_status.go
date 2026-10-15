package cursor

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

// SpendLimit reports upstream USD, never new-api quota or an account balance.
// Unknown fields stay nil; remaining is computed in integer cents before display.
type SpendLimit struct {
	Scope        string   `json:"scope"`
	UsedUSD      *float64 `json:"used_usd"`
	LimitUSD     *float64 `json:"limit_usd"`
	RemainingUSD *float64 `json:"remaining_usd"`
	State        string   `json:"state"`
}
type OnDemandBudget struct {
	State  string       `json:"state"`
	Limits []SpendLimit `json:"limits"`
}

// Cursor dashboard cents may be JSON numbers or int64 strings. Reject values
// outside exact JS integer range, fractional cents and malformed/negative data.
func dashboardCents(raw json.RawMessage, dollars bool) *int64 {
	var number json.Number
	if len(raw) == 0 || len(raw) > 64 || string(raw) == "null" || common.Unmarshal(raw, &number) != nil {
		return nil
	}
	value, err := decimal.NewFromString(number.String())
	if err != nil || value.Exponent() < -6 || value.Exponent() > 16 {
		return nil
	}
	if dollars {
		value = value.Shift(2)
	}
	if value.IsNegative() || value.GreaterThan(decimal.NewFromInt(9007199254740991)) || !value.Equal(value.Truncate(0)) {
		return nil
	}
	cents := value.IntPart()
	return &cents
}
func spendLimit(scope string, used, limit *int64) SpendLimit {
	out := SpendLimit{Scope: scope, State: "unknown"}
	if used != nil {
		n := float64(*used) / 100
		out.UsedUSD = &n
	}
	if limit != nil {
		n := float64(*limit) / 100
		out.LimitUSD = &n
	}
	if used != nil && limit != nil {
		n := float64(max(int64(0), *limit-*used)) / 100
		out.RemainingUSD = &n
		out.State = "remaining"
		if *used >= *limit {
			out.State = "exhausted"
		}
	}
	return out
}

// parseOnDemand requires explicit settings. A shared team budget never overrides
// an exhausted member limit, and omitted member limits are not called unlimited.
func parseOnDemand(spend, hardBody json.RawMessage) (string, OnDemandBudget) {
	result := OnDemandBudget{State: "unknown", Limits: []SpendLimit{}}
	var usage, hard map[string]json.RawMessage
	if common.Unmarshal(spend, &usage) != nil {
		usage = nil
	}
	if common.Unmarshal(hardBody, &hard) != nil {
		hard = nil
	}
	var noUsage *bool
	if common.Unmarshal(hard["noUsageBasedAllowed"], &noUsage) != nil {
		noUsage = nil
	}
	var kind string
	_ = common.Unmarshal(usage["limitType"], &kind)
	own := dashboardCents(usage["individualLimit"], false)
	used := dashboardCents(usage["individualUsed"], false)
	cap := dashboardCents(hard["hardLimit"], true)
	enabled := "unknown"
	if noUsage != nil && !*noUsage || cap != nil && *cap > 0 {
		enabled = "enabled"
	}
	if noUsage != nil && *noUsage || own != nil && *own == 0 || cap != nil && *cap == 0 {
		enabled = "disabled"
	}
	if kind == "team" {
		result.Limits = append(result.Limits, spendLimit("member", used, own), spendLimit("team", dashboardCents(usage["pooledUsed"], false), dashboardCents(usage["pooledLimit"], false)))
	} else if kind == "user" || kind == "individual" {
		// Both reported constraints apply; keep them separate if Cursor reports both.
		if own != nil {
			result.Limits = append(result.Limits, spendLimit("member", used, own))
		}
		result.Limits = append(result.Limits, spendLimit("account", used, cap))
	} else {
		// Unknown scope cannot be safely treated as a personal account or team.
		if cap != nil {
			result.Limits = append(result.Limits, spendLimit("account", nil, cap))
		}
	}
	if enabled == "disabled" {
		result.State = "disabled"
		return enabled, result
	}
	allKnown := len(result.Limits) > 0
	// A disagreement between two reported team caps cannot establish paid headroom.
	if pool := dashboardCents(usage["pooledLimit"], false); kind == "team" && cap != nil && pool != nil && *cap != *pool {
		allKnown = false
	}
	for _, limit := range result.Limits {
		if limit.State == "exhausted" {
			result.State = "exhausted"
			return enabled, result
		}
		allKnown = allKnown && limit.State == "remaining"
	}
	if enabled == "enabled" && allKnown {
		result.State = "remaining"
	}
	return enabled, result
}

// Only a valid future upstream cycle end is a known reset. An elapsed date is
// retained as evidence, never advanced by inventing a monthly/weekly cadence.
func quotaReset(raw string, now time.Time) (*time.Time, string) {
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms <= 0 || ms > 253402300799999 {
		return nil, "unknown"
	}
	at := time.UnixMilli(ms).UTC()
	if !at.After(now) {
		return &at, "elapsed"
	}
	return &at, "known"
}
