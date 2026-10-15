package cursor

import (
	"context"
	"crypto/sha256"
	"strings"
	"time"
)

type health struct {
	inFlight, failures int
	openUntil          time.Time
	probing            bool
	lastRequest        *RequestObservation
}

func (h *health) unavailable(now time.Time, limit int) bool {
	return now.Before(h.openUntil) || h.inFlight >= limit || (!h.openUntil.IsZero() && h.inFlight > 0)
}

func (h *health) acquire(now time.Time, limit int) int {
	if h.unavailable(now, limit) {
		return 429
	}
	h.probing = !h.openUntil.IsZero()
	h.inFlight++
	return 0
}

// RoutingStatus is a cached scheduling hint, not an admission reservation.
// Chat atomically checks the same gates again before sending upstream traffic.
type RoutingStatus struct {
	Blocked      string
	Scope        BudgetScope
	NeedsProbe   bool
	QuotaLimited bool
}

// Routing reads no credentials from disk and makes no upstream calls. A stale
// catalog can identify a quota pool, but Chat still refreshes model permissions.
// Missing/expired quota and unknown paid headroom never exclude an account.
func (c *Client) Routing(key, modelID string) RoutingStatus {
	c.mu.Lock()
	a := c.accounts[sha256.Sum256([]byte(strings.TrimSpace(key)))]
	c.mu.Unlock()
	result := RoutingStatus{}
	if a == nil {
		return result
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := c.now()
	if a.catalog != nil {
		if m, ok := a.catalog.Model(modelID); ok {
			result.Scope = m.BudgetScope
		}
	}
	if a.disabled {
		result.Blocked = "account_disabled"
		return result
	}
	for _, gate := range []struct {
		name string
		h    *health
	}{{"account", &a.health}, {"quota_pool", a.windows[result.Scope]}} {
		if gate.h == nil {
			continue
		}
		if gate.h.unavailable(now, c.maxInFlight) {
			result.Blocked = gate.name + "_busy"
			if now.Before(gate.h.openUntil) {
				result.Blocked = gate.name + "_cooling"
			}
			return result
		}
		result.NeedsProbe = result.NeedsProbe || !gate.h.openUntil.IsZero()
	}
	q := a.quota
	if !result.NeedsProbe && q != nil && now.Before(q.ExpiresAt) && (q.OnDemandBudget.State == "exhausted" || q.OnDemandBudget.State == "disabled") {
		for _, window := range q.Windows {
			if result.Scope != "" && window.Name == string(result.Scope) && window.UsedPercent >= 100 {
				result.QuotaLimited = true
			}
		}
	}
	return result
}
func (h *health) finish(now time.Time, status int, retry time.Duration, neutral bool) {
	h.inFlight--
	probe := h.probing
	h.probing = false
	if neutral {
		return
	}
	var cooldown time.Duration
	switch {
	case status == 429:
		cooldown = max(30*time.Second, retry)
		h.failures++
	case status >= 500:
		h.failures++
		if h.failures >= 2 {
			cooldown = 30 * time.Second
		}
	case status >= 200 && status < 300:
		if h.openUntil.IsZero() || probe {
			h.failures = 0
			h.openUntil = time.Time{}
		}
	}
	if until := now.Add(cooldown); cooldown > 0 && until.After(h.openUntil) {
		h.openUntil = until
	}
}
func (c *Client) acquireWindow(a *account, scope BudgetScope) int {
	if scope == "" {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.windows == nil {
		a.windows = map[BudgetScope]*health{}
	}
	if a.windows[scope] == nil {
		a.windows[scope] = &health{}
	}
	return a.windows[scope].acquire(c.now(), c.maxInFlight)
}
func (c *Client) finishRun(ctx context.Context, key string, a *account, scope BudgetScope, status int, retry time.Duration, neutral bool) string {
	isolated := false
	if !neutral && status == 429 && scope != "" {
		quota, err := c.Quota(ctx, key)
		isolated = err == nil && quota.isolatedExhaustion(scope, c.now())
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if status == 401 && !neutral {
		a.disabled = true
		a.token = ""
	}
	if !neutral {
		h := &a.health
		if window := a.windows[scope]; window != nil {
			h = window
		}
		h.lastRequest = &RequestObservation{Scope: string(scope), ObservedAt: c.now().UTC(), Status: status}
	}
	a.health.finish(c.now(), status, retry, neutral || isolated)
	if h := a.windows[scope]; h != nil {
		// Account failures do not create a second, redundant window circuit.
		h.finish(c.now(), status, retry, neutral || (status >= 400 && !isolated))
	}
	if isolated {
		return string(scope) + " included quota is exhausted and Cursor returned 429; this quota window is cooling down"
	}
	return statusMessage(status)
}

type CooldownStatus struct {
	Scope   string    `json:"scope"`
	RetryAt time.Time `json:"retry_at"`
	State   string    `json:"state"`
}
type RequestObservation struct {
	Scope      string    `json:"scope"`
	ObservedAt time.Time `json:"observed_at"`
	Status     int       `json:"status"`
}
type AccountHealth struct {
	LastRequests []RequestObservation `json:"last_requests"`
	Disabled     bool                 `json:"disabled"`
	InFlight     int                  `json:"in_flight"`
	MaxInFlight  int                  `json:"max_in_flight"`
	Cooldowns    []CooldownStatus     `json:"cooldowns"`
}

func (c *Client) Health(key string) (AccountHealth, error) {
	a, err := c.account(key)
	if err != nil {
		return AccountHealth{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	result := AccountHealth{Disabled: a.disabled, InFlight: a.inFlight, MaxInFlight: c.maxInFlight, Cooldowns: []CooldownStatus{}}
	for _, v := range []struct {
		scope string
		h     *health
	}{{"account", &a.health}, {string(CursorModels), a.windows[CursorModels]}, {string(OtherModels), a.windows[OtherModels]}} {
		if v.h != nil && v.h.lastRequest != nil {
			result.LastRequests = append(result.LastRequests, *v.h.lastRequest)
		}
		if v.h == nil || v.h.openUntil.IsZero() {
			continue
		}
		state := "cooling"
		if !c.now().Before(v.h.openUntil) {
			state = "awaiting_probe"
		}
		result.Cooldowns = append(result.Cooldowns, CooldownStatus{v.scope, v.h.openUntil, state})
	}
	return result, nil
}
