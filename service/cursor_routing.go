package service

import (
	"crypto/sha256"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	native "github.com/QuantumNous/new-api/pkg/cursor"
	hostreasoning "github.com/QuantumNous/new-api/setting/reasoning"
	"github.com/gin-gonic/gin"
)

// cursorRoutingStatus uses the same mapping chain as the relay. Malformed or
// cyclic mappings remain unknown here; the relay reports the configuration error.
func cursorRoutingStatus(c *gin.Context, channel *model.Channel, modelName string) native.RoutingStatus {
	if channel.Type != constant.ChannelTypeCursor {
		return native.RoutingStatus{}
	}
	if channel.ChannelInfo.IsMultiKey {
		return native.RoutingStatus{Blocked: "requires_one_account_per_channel"}
	}
	state := RequestPolicy(c)
	state.mu.Lock()
	failed := state.failedCursorAccounts[sha256.Sum256([]byte(strings.TrimSpace(channel.Key)))]
	state.mu.Unlock()
	if failed {
		return native.RoutingStatus{Blocked: "account_already_failed"}
	}
	var mapping map[string]string
	if raw := channel.GetModelMapping(); raw != "" && raw != "{}" {
		if common.UnmarshalJsonStr(raw, &mapping) != nil {
			modelName = ""
		}
	}
	seen := map[string]bool{modelName: true}
	for {
		next := mapping[modelName]
		if next == "" {
			next = mapping[hostreasoning.BaseModelName(modelName)]
		}
		if next == "" || next == modelName {
			break
		}
		if seen[next] {
			modelName = ""
			break
		}
		seen[next], modelName = true, next
	}
	return native.Default.Routing(channel.Key, modelName)
}

// cursorCandidatePolicy keeps provider state out of the database layer. The
// input already satisfies group/model/path constraints. No upstream I/O occurs.
func cursorCandidatePolicy(c *gin.Context, modelName string) model.ChannelCandidatePolicy {
	return func(candidates []*model.Channel) []*model.Channel {
		hasCursor := false
		onlyCursor := true
		for _, channel := range candidates {
			hasCursor = hasCursor || channel.Type == constant.ChannelTypeCursor
			onlyCursor = onlyCursor && channel.Type == constant.ChannelTypeCursor
		}
		if !hasCursor {
			return candidates
		}
		eligible := make([]*model.Channel, 0, len(candidates))
		limited := make(map[int]bool)
		for _, channel := range candidates {
			status := cursorRoutingStatus(c, channel, modelName)
			if status.Blocked != "" {
				RequestPolicy(c).AddEvent(PolicyEvent{ChannelID: channel.Id, Decision: PolicyDecision{Action: "skip", Reason: status.Blocked, Source: "cursor_pool"}})
				continue
			}
			eligible = append(eligible, channel)
			limited[channel.Id] = status.QuotaLimited
		}
		if len(eligible) == 0 {
			return eligible
		}
		// Mixed-provider groups keep the host's priority-per-retry semantics.
		// Cursor health/exclusion still applies, without repeating another
		// provider's failed primary by resetting its priority progression.
		if !onlyCursor {
			return eligible
		}
		priority := eligible[0].GetPriority()
		for _, channel := range eligible {
			priority = max(priority, channel.GetPriority())
		}
		preferHeadroom := false
		for _, channel := range eligible {
			preferHeadroom = preferHeadroom || channel.GetPriority() == priority && !limited[channel.Id]
		}
		selected := make([]*model.Channel, 0, len(eligible))
		for _, channel := range eligible {
			if channel.GetPriority() != priority {
				continue
			}
			if preferHeadroom && limited[channel.Id] {
				RequestPolicy(c).AddEvent(PolicyEvent{ChannelID: channel.Id, Decision: PolicyDecision{Action: "skip", Reason: "quota_deprioritized", Source: "cursor_pool"}})
				continue
			}
			selected = append(selected, channel)
		}
		// Only the highest remaining priority is returned. A retry exhausts
		// distinct healthy accounts in that tier before descending to backups.
		return selected
	}
}

func recordCursorAccountFailure(c *gin.Context) {
	if common.GetContextKeyInt(c, constant.ContextKeyChannelType) != constant.ChannelTypeCursor {
		return
	}
	key := strings.TrimSpace(common.GetContextKeyString(c, constant.ContextKeyChannelKey))
	if key == "" {
		return
	}
	state := RequestPolicy(c)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.failedCursorAccounts == nil {
		state.failedCursorAccounts = make(map[[32]byte]bool)
	}
	state.failedCursorAccounts[sha256.Sum256([]byte(key))] = true
}
