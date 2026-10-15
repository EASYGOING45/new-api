package controller

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	native "github.com/QuantumNous/new-api/pkg/cursor"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay"
	"github.com/QuantumNous/new-api/relay/channel/ai360"
	"github.com/QuantumNous/new-api/relay/channel/lingyiwanwu"
	"github.com/QuantumNous/new-api/relay/channel/minimax"
	"github.com/QuantumNous/new-api/relay/channel/moonshot"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

// https://platform.openai.com/docs/api-reference/models/list

var openAIModels []dto.OpenAIModels
var openAIModelsMap map[string]dto.OpenAIModels
var channelId2Models map[int][]string

func init() {
	// https://platform.openai.com/docs/models/model-endpoint-compatibility
	for i := range constant.APITypeDummy {
		if i == constant.APITypeAIProxyLibrary {
			continue
		}
		adaptor := relay.GetAdaptor(i)
		channelName := adaptor.GetChannelName()
		modelNames := adaptor.GetModelList()
		for _, modelName := range modelNames {
			openAIModels = append(openAIModels, dto.OpenAIModels{
				Id:      modelName,
				Object:  "model",
				Created: 1626777600,
				OwnedBy: channelName,
			})
		}
	}
	for _, modelName := range ai360.ModelList {
		openAIModels = append(openAIModels, dto.OpenAIModels{
			Id:      modelName,
			Object:  "model",
			Created: 1626777600,
			OwnedBy: ai360.ChannelName,
		})
	}
	for _, modelName := range moonshot.ModelList {
		openAIModels = append(openAIModels, dto.OpenAIModels{
			Id:      modelName,
			Object:  "model",
			Created: 1626777600,
			OwnedBy: moonshot.ChannelName,
		})
	}
	for _, modelName := range lingyiwanwu.ModelList {
		openAIModels = append(openAIModels, dto.OpenAIModels{
			Id:      modelName,
			Object:  "model",
			Created: 1626777600,
			OwnedBy: lingyiwanwu.ChannelName,
		})
	}
	for _, modelName := range minimax.ModelList {
		openAIModels = append(openAIModels, dto.OpenAIModels{
			Id:      modelName,
			Object:  "model",
			Created: 1626777600,
			OwnedBy: minimax.ChannelName,
		})
	}
	for modelName, _ := range constant.MidjourneyModel2Action {
		openAIModels = append(openAIModels, dto.OpenAIModels{
			Id:      modelName,
			Object:  "model",
			Created: 1626777600,
			OwnedBy: "midjourney",
		})
	}
	openAIModelsMap = make(map[string]dto.OpenAIModels)
	for _, aiModel := range openAIModels {
		openAIModelsMap[aiModel.Id] = aiModel
	}
	channelId2Models = make(map[int][]string)
	for i := 1; i <= constant.ChannelTypeDummy; i++ {
		apiType, success := common.ChannelType2APIType(i)
		if !success || apiType == constant.APITypeAIProxyLibrary {
			if plugin, ok := jsplugin.DefaultRegistry.GetByChannelType(i); ok {
				channelId2Models[i] = append([]string(nil), plugin.Meta.Models...)
			}
			continue
		}
		meta := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: i,
		}}
		adaptor := relay.GetAdaptor(apiType)
		adaptor.Init(meta)
		channelId2Models[i] = adaptor.GetModelList()
		if len(channelId2Models[i]) == 0 {
			if plugin, ok := jsplugin.DefaultRegistry.GetByChannelType(i); ok {
				channelId2Models[i] = append([]string(nil), plugin.Meta.Models...)
			}
		}
	}
	openAIModels = lo.UniqBy(openAIModels, func(m dto.OpenAIModels) string {
		return m.Id
	})
}

func channelOwnerName(channelType int) string {
	apiType, success := common.ChannelType2APIType(channelType)
	if !success {
		return strings.ToLower(constant.GetChannelTypeName(channelType))
	}
	adaptor := relay.GetAdaptor(apiType)
	if adaptor == nil {
		return strings.ToLower(constant.GetChannelTypeName(channelType))
	}
	adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType: channelType,
	}})
	if name := strings.TrimSpace(adaptor.GetChannelName()); name != "" {
		return name
	}
	return strings.ToLower(constant.GetChannelTypeName(channelType))
}

func getPreferredModelOwners(modelNames []string, groups []string) map[string]string {
	channelTypes, err := model.GetPreferredModelOwnerChannelTypes(modelNames, groups)
	if err != nil {
		common.SysLog(common.LogText("GetPreferredModelOwnerChannelTypes error: %v", err))
		return map[string]string{}
	}

	ownerByChannelType := make(map[int]string)
	owners := make(map[string]string, len(channelTypes))
	for modelName, channelType := range channelTypes {
		owner, ok := ownerByChannelType[channelType]
		if !ok {
			owner = channelOwnerName(channelType)
			ownerByChannelType[channelType] = owner
		}
		if owner != "" {
			owners[modelName] = owner
		}
	}
	return owners
}

// getCursorModelCapabilities only discovers accounts reachable through the
// caller's authorized groups and configured models. No key or channel ID is
// returned. Mixed transports cannot promise a common Cursor parameter contract.
func getCursorModelCapabilities(ctx context.Context, names, groups []string, owners map[string]string) map[string]native.CatalogModel {
	result := map[string]native.CatalogModel{}
	wanted := map[string]bool{}
	for _, name := range names {
		if owners[name] == "cursor" {
			wanted[name] = true
		}
	}
	if len(wanted) == 0 {
		return result
	}
	abilities, err := model.GetAllEnableAbilityWithChannels()
	if err != nil {
		for name := range wanted {
			result[name] = native.CatalogModel{ID: name, Provider: "cursor", Error: "Cursor catalog is unavailable"}
		}
		return result
	}
	channelIDs := map[int]bool{}
	for _, a := range abilities {
		if wanted[a.Model] && slices.Contains(groups, a.Group) && a.ChannelType == constant.ChannelTypeCursor {
			channelIDs[a.ChannelId] = true
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	catalogs := map[int]*native.Catalog{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	limit := make(chan struct{}, 4)
	for id := range channelIDs {
		wg.Go(func() {
			select {
			case limit <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-limit }()
			channel, err := model.CacheGetChannel(id)
			if err != nil || channel.Status != common.ChannelStatusEnabled {
				return
			}
			catalog, err := service.FetchCursorChannelCatalog(ctx, channel, false)
			if err == nil {
				mu.Lock()
				catalogs[id] = catalog
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	for _, a := range abilities {
		if !wanted[a.Model] || !slices.Contains(groups, a.Group) {
			continue
		}
		entry := native.CatalogModel{ID: a.Model, Provider: "cursor", Error: "Cursor catalog is unavailable for this route"}
		if a.ChannelType == constant.ChannelTypeCursor {
			if catalog := catalogs[a.ChannelId]; catalog != nil {
				if found, ok := catalog.Model(a.Model); ok {
					entry = found
				}
			}
		}
		if prior, ok := result[a.Model]; ok {
			entry = prior.Intersect(entry)
		}
		result[a.Model] = entry
	}
	for name := range wanted {
		if _, ok := result[name]; !ok {
			result[name] = native.CatalogModel{ID: name, Provider: "cursor", Error: "Cursor catalog is unavailable for this route"}
		}
	}
	for name, cap := range result {
		if cap.Error == "" {
			owners[name] = cap.Provider
		}
	}
	return result
}

func buildOpenAIModel(modelName string, ownerByModel map[string]string) dto.OpenAIModels {
	var oaiModel dto.OpenAIModels
	if staticModel, ok := openAIModelsMap[modelName]; ok {
		oaiModel = staticModel
	} else {
		oaiModel = dto.OpenAIModels{
			Id:      modelName,
			Object:  "model",
			Created: 1626777600,
			OwnedBy: "custom",
		}
	}
	if owner, ok := ownerByModel[modelName]; ok && owner != "" {
		oaiModel.OwnedBy = owner
	}
	oaiModel.SupportedEndpointTypes = model.GetModelSupportEndpointTypes(modelName)
	return oaiModel
}

type modelListGroups struct {
	userGroup   string
	tokenGroup  string
	ownerGroups []string
}

func getModelListGroups(c *gin.Context) (modelListGroups, error) {
	tokenGroup := common.GetContextKeyString(c, constant.ContextKeyTokenGroup)
	userGroup := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
	if userGroup == "" && (tokenGroup == "" || tokenGroup == "auto") {
		var err error
		userGroup, err = model.GetUserGroup(c.GetInt("id"), false)
		if err != nil {
			return modelListGroups{}, err
		}
	}

	if tokenGroup == "auto" {
		return modelListGroups{
			userGroup:   userGroup,
			tokenGroup:  tokenGroup,
			ownerGroups: service.GetRequestAutoGroups(c, userGroup),
		}, nil
	}

	group := userGroup
	if tokenGroup != "" {
		group = tokenGroup
	}
	return modelListGroups{
		userGroup:   userGroup,
		tokenGroup:  tokenGroup,
		ownerGroups: []string{group},
	}, nil
}

func ListModels(c *gin.Context, modelType int) {
	acceptUnsetRatioModel := operation_setting.SelfUseModeEnabled
	if !acceptUnsetRatioModel {
		userId := c.GetInt("id")
		if userId > 0 {
			userSettings, _ := model.GetUserSetting(userId, false)
			if userSettings.AcceptUnsetRatioModel {
				acceptUnsetRatioModel = true
			}
		}
	}

	userModelNames := make([]string, 0)
	groups, err := getModelListGroups(c)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "get user group failed",
		})
		return
	}
	ownerGroups := groups.ownerGroups
	modelLimitEnable := common.GetContextKeyBool(c, constant.ContextKeyTokenModelLimitEnabled)
	var tokenModelLimit map[string]bool
	if modelLimitEnable {
		s, ok := common.GetContextKey(c, constant.ContextKeyTokenModelLimit)
		if ok {
			tokenModelLimit, _ = s.(map[string]bool)
		}
		if tokenModelLimit == nil {
			tokenModelLimit = map[string]bool{}
		}
	}
	models := service.GetGroupsEnabledModels(ownerGroups)
	for _, modelName := range models {
		if modelLimitEnable {
			matchingName := ratio_setting.RoutingMatchModelName(modelName)
			if !tokenModelLimit[modelName] && !tokenModelLimit[matchingName] {
				continue
			}
		}
		if !acceptUnsetRatioModel && !helper.HasModelBillingConfig(modelName) {
			continue
		}
		userModelNames = append(userModelNames, modelName)
	}

	ownerByModel := map[string]string{}
	if len(ownerGroups) > 0 {
		ownerByModel = getPreferredModelOwners(userModelNames, ownerGroups)
	}
	capabilities := getCursorModelCapabilities(c.Request.Context(), userModelNames, ownerGroups, ownerByModel)
	userOpenAiModels := make([]dto.OpenAIModels, 0, len(userModelNames))
	for _, modelName := range userModelNames {
		userOpenAiModels = append(userOpenAiModels, buildOpenAIModel(modelName, ownerByModel))
	}

	switch modelType {
	case constant.ChannelTypeAnthropic:
		useranthropicModels := make([]dto.AnthropicModel, len(userOpenAiModels))
		for i, model := range userOpenAiModels {
			useranthropicModels[i] = dto.AnthropicModel{
				ID:          model.Id,
				CreatedAt:   time.Unix(int64(model.Created), 0).UTC().Format(time.RFC3339),
				DisplayName: model.Id,
				Type:        "model",
			}
		}
		firstID := ""
		lastID := ""
		if len(useranthropicModels) > 0 {
			firstID = useranthropicModels[0].ID
			lastID = useranthropicModels[len(useranthropicModels)-1].ID
		}
		c.JSON(200, gin.H{
			"data":     useranthropicModels,
			"first_id": firstID,
			"has_more": false,
			"last_id":  lastID,
		})
	case constant.ChannelTypeGemini:
		userGeminiModels := make([]dto.GeminiModel, len(userOpenAiModels))
		for i, model := range userOpenAiModels {
			userGeminiModels[i] = dto.GeminiModel{
				Name:        model.Id,
				DisplayName: model.Id,
			}
		}
		c.JSON(200, gin.H{
			"models":        userGeminiModels,
			"nextPageToken": nil,
		})
	default:
		type listedModel struct {
			dto.OpenAIModels
			Cursor *native.CatalogModel `json:"cursor,omitempty"`
		}
		listed := make([]listedModel, 0, len(userOpenAiModels))
		for _, m := range userOpenAiModels {
			item := listedModel{OpenAIModels: m}
			if cap, ok := capabilities[m.Id]; ok {
				item.Cursor = &cap
			}
			listed = append(listed, item)
		}
		c.JSON(200, gin.H{
			"success": true,
			"data":    listed,
			"object":  "list",
		})
	}
}

func ChannelListModels(c *gin.Context) {
	c.JSON(200, gin.H{
		"success": true,
		"data":    openAIModels,
	})
}

func DashboardListModels(c *gin.Context) {
	modelsByChannel := make(map[int][]string, len(channelId2Models))
	for channelType, models := range channelId2Models {
		modelsByChannel[channelType] = append([]string(nil), models...)
	}
	for channelType := 1; channelType <= constant.ChannelTypeDummy; channelType++ {
		if plugin, ok := jsplugin.DefaultRegistry.GetByChannelType(channelType); ok {
			modelsByChannel[channelType] = append([]string(nil), plugin.Meta.Models...)
		}
	}
	c.JSON(200, gin.H{
		"success": true,
		"data":    modelsByChannel,
	})
}

func EnabledListModels(c *gin.Context) {
	c.JSON(200, gin.H{
		"success": true,
		"data":    model.GetEnabledModels(),
	})
}

func RetrieveModel(c *gin.Context, modelType int) {
	modelId := c.Param("model")
	if aiModel, ok := openAIModelsMap[modelId]; ok {
		switch modelType {
		case constant.ChannelTypeAnthropic:
			c.JSON(200, dto.AnthropicModel{
				ID:          aiModel.Id,
				CreatedAt:   time.Unix(int64(aiModel.Created), 0).UTC().Format(time.RFC3339),
				DisplayName: aiModel.Id,
				Type:        "model",
			})
		default:
			c.JSON(200, aiModel)
		}
	} else {
		openAIError := types.OpenAIError{
			Message: fmt.Sprintf("The model '%s' does not exist", modelId),
			Type:    "invalid_request_error",
			Param:   "model",
			Code:    "model_not_found",
		}
		c.JSON(200, gin.H{
			"error": openAIError,
		})
	}
}

// CursorRouteStatus intentionally omits channel/account identifiers. An account
// shared by channels is counted once; percentages are never summed or averaged.
type CursorRouteStatus struct {
	Route  string                `json:"route"`
	Quota  *native.QuotaSnapshot `json:"quota"`
	Health native.AccountHealth  `json:"health"`
	Error  string                `json:"error,omitempty"`
}

func getCursorRouteStatus(ctx context.Context, name string, groups []string) []CursorRouteStatus {
	result := []CursorRouteStatus{}
	abilities, err := model.GetAllEnableAbilityWithChannels()
	if err != nil {
		return []CursorRouteStatus{{Route: "unavailable", Error: "Cursor quota is unavailable"}}
	}
	ids := []int{}
	for _, ability := range abilities {
		if ability.Model == name && slices.Contains(groups, ability.Group) && ability.ChannelType == constant.ChannelTypeCursor && !slices.Contains(ids, ability.ChannelId) {
			ids = append(ids, ability.ChannelId)
		}
	}
	slices.Sort(ids)
	seen := map[string]bool{}
	channels := []*model.Channel{}
	for _, id := range ids {
		channel, err := model.CacheGetChannel(id)
		if err != nil || channel.Status != common.ChannelStatusEnabled {
			continue
		}
		key := strings.TrimSpace(channel.Key)
		if seen[key] {
			continue
		}
		seen[key] = true
		channels = append(channels, channel)
	}
	result = make([]CursorRouteStatus, len(channels))
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	limit := make(chan struct{}, 4)
	for i, channel := range channels {
		result[i].Route = fmt.Sprint(i + 1)
		wg.Go(func() {
			select {
			case limit <- struct{}{}:
			case <-ctx.Done():
				result[i].Error = "Cursor quota is unavailable"
				return
			}
			defer func() { <-limit }()
			quota, health, err := service.FetchCursorChannelStatus(ctx, channel)
			result[i] = CursorRouteStatus{Route: fmt.Sprint(i + 1), Quota: quota, Health: health}
			if err != nil {
				result[i].Error = "Cursor quota is unavailable"
			}
		})
	}
	wg.Wait()
	return result
}
