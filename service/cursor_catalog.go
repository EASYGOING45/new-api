package service

import (
	"context"
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	native "github.com/QuantumNous/new-api/pkg/cursor"
)

// FetchCursorChannelCatalog keeps discovery on the same fixed, authenticated
// transport as inference. Each channel represents one account.
func FetchCursorChannelCatalog(ctx context.Context, channel *model.Channel, force bool) (*native.Catalog, error) {
	if channel.Type != constant.ChannelTypeCursor || (channel.GetBaseURL() != "" && channel.GetBaseURL() != "https://api2.cursor.sh") {
		return nil, errors.New("Cursor native channel requires the fixed Cursor service address")
	}
	key := strings.TrimSpace(channel.Key)
	if channel.ChannelInfo.IsMultiKey || strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("Cursor model discovery requires one account per channel")
	}
	return native.Default.Catalog(ctx, key, force)
}

// FetchCursorChannelStatus exposes only operational metadata, never credentials.
func FetchCursorChannelStatus(ctx context.Context, channel *model.Channel) (*native.QuotaSnapshot, native.AccountHealth, error) {
	if channel.Type != constant.ChannelTypeCursor || (channel.GetBaseURL() != "" && channel.GetBaseURL() != "https://api2.cursor.sh") || channel.ChannelInfo.IsMultiKey || strings.ContainsAny(strings.TrimSpace(channel.Key), "\r\n") {
		return nil, native.AccountHealth{}, errors.New("Cursor status requires one native account per channel")
	}
	key := strings.TrimSpace(channel.Key)
	quota, err := native.Default.Quota(ctx, key)
	health, _ := native.Default.Health(key)
	return quota, health, err
}
