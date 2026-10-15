package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// Budget scopes follow the authenticated picker vendor, never model-name prefixes.
// Unknown vendors remain unclassified until their billing scope is established.
type BudgetScope string

const (
	CursorModels BudgetScope = "Cursor Models"
	OtherModels  BudgetScope = "Other Models"
)

func vendorBudgetScope(vendor string) BudgetScope {
	switch vendor {
	case "cursor":
		return CursorModels
	case "anthropic", "openai", "google", "meta", "moonshot", "zai":
		return OtherModels
	default:
		return ""
	}
}

type QuotaWindow struct {
	Name        string  `json:"name"`
	UsedPercent float64 `json:"used_percent"`
}
type QuotaSnapshot struct {
	Source            string         `json:"source"`
	ObservedAt        time.Time      `json:"observed_at"`
	ExpiresAt         time.Time      `json:"expires_at"`
	Windows           []QuotaWindow  `json:"windows"`
	BillingCycleEndMS string         `json:"billing_cycle_end_ms"`
	OnDemand          string         `json:"on_demand"`
	OnDemandBudget    OnDemandBudget `json:"on_demand_budget"`
	ResetsAt          *time.Time     `json:"resets_at"`
	ResetState        string         `json:"reset_state"`
	MonetaryBalance   *float64       `json:"monetary_balance"`
}

// Quota coalesces reads per account for 30 seconds. Missing or failed observations
// never become zero usage, and expired snapshots are not used for admission.
// A percentage is included usage, not money or proof of on-demand availability.
func (c *Client) Quota(ctx context.Context, key string) (*QuotaSnapshot, error) {
	a, err := c.account(key)
	if err != nil {
		return nil, err
	}
	for {
		a.mu.Lock()
		if a.quota != nil && c.now().Before(a.quota.ExpiresAt) {
			q := a.quota
			a.mu.Unlock()
			return q, nil
		}
		if c.now().Before(a.quotaRetryAt) {
			err := a.quotaError
			a.mu.Unlock()
			return nil, err
		}
		if loading := a.quotaLoading; loading != nil {
			a.mu.Unlock()
			select {
			case <-loading:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		a.quotaLoading = make(chan struct{})
		a.mu.Unlock()
		snapshot, err := c.fetchQuota(ctx, a, key)
		a.mu.Lock()
		a.quota, a.quotaError = snapshot, err
		if err != nil {
			a.quotaRetryAt = c.now().Add(10 * time.Second)
		} else {
			a.quotaRetryAt = time.Time{}
		}
		close(a.quotaLoading)
		a.quotaLoading = nil
		a.mu.Unlock()
		return snapshot, err
	}
}
func (c *Client) fetchQuota(ctx context.Context, a *account, key string) (*QuotaSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	token, _, status := c.authenticate(ctx, a, key)
	if status != 200 {
		return nil, errors.New(statusMessage(status))
	}
	// Billing settings are optional and bounded separately; their failure must
	// not erase a successful included-usage response.
	hardResult := make(chan []byte, 1)
	go func() {
		hardCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		body, _, _ := c.post(hardCtx, "/aiserver.v1.DashboardService/GetHardLimit", token)
		hardResult <- body
	}()
	b, _, err := c.post(ctx, "/aiserver.v1.DashboardService/GetCurrentPeriodUsage", token)
	if err != nil {
		return nil, err
	}
	var data struct {
		PlanUsage *struct {
			Auto  *float64 `json:"autoPercentUsed"`
			API   *float64 `json:"apiPercentUsed"`
			Total *float64 `json:"totalPercentUsed"`
		} `json:"planUsage"`
		End   json.RawMessage `json:"billingCycleEnd"`
		Spend json.RawMessage `json:"spendLimitUsage"`
	}
	if common.Unmarshal(b, &data) != nil {
		return nil, errors.New("invalid Cursor quota response")
	}
	var end string
	_ = common.Unmarshal(data.End, &end)
	now := c.now().UTC()
	result := &QuotaSnapshot{Source: "cursor-native-experimental", ObservedAt: now, ExpiresAt: now.Add(30 * time.Second), Windows: []QuotaWindow{}, BillingCycleEndMS: end, OnDemand: "unknown"}
	result.OnDemand, result.OnDemandBudget = parseOnDemand(data.Spend, <-hardResult)
	result.ResetsAt, result.ResetState = quotaReset(end, now)
	if data.PlanUsage != nil {
		for _, v := range []struct {
			name string
			used *float64
		}{{string(CursorModels), data.PlanUsage.Auto}, {string(OtherModels), data.PlanUsage.API}, {"Total", data.PlanUsage.Total}} {
			if v.used != nil && *v.used >= 0 && !math.IsNaN(*v.used) && !math.IsInf(*v.used, 0) {
				result.Windows = append(result.Windows, QuotaWindow{v.name, *v.used})
			}
		}
	}
	return result, nil
}

// isolatedExhaustion is conservative: an upstream 429 can be assigned to one
// included pool only with a fresh exhausted observation AND a non-exhausted peer.
// This is a correlation, not an upstream scope field. Ambiguous/missing evidence
// retains account-wide cooldown. 100% alone never blocks paid on-demand usage.
func (q *QuotaSnapshot) isolatedExhaustion(scope BudgetScope, now time.Time) bool {
	if q == nil || !now.Before(q.ExpiresAt) || (scope != CursorModels && scope != OtherModels) {
		return false
	}
	// Positive paid headroom contradicts attributing a 429 to included exhaustion.
	if q.OnDemandBudget.State == "remaining" {
		return false
	}
	var exhausted, peerAvailable bool
	for _, w := range q.Windows {
		if w.Name == string(scope) {
			exhausted = w.UsedPercent >= 100
		}
		if (w.Name == string(CursorModels) || w.Name == string(OtherModels)) && w.Name != string(scope) {
			peerAvailable = w.UsedPercent < 100
		}
	}
	return exhausted && peerAvailable
}
