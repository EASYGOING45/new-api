package controller

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// Bound management reads across pages and administrators. This does not consume
// inference slots or reset the account's cooldown, quota cache, or health state.
var cursorStatusReads = make(chan struct{}, 4)

func GetCursorChannelStatus(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	channel, err := model.GetChannelById(id, true)
	if err != nil || channel.Type != constant.ChannelTypeCursor {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	if channel.ChannelInfo.IsMultiKey || strings.TrimSpace(channel.Key) == "" || strings.ContainsAny(strings.TrimSpace(channel.Key), "\r\n") || (channel.GetBaseURL() != "" && channel.GetBaseURL() != "https://api2.cursor.sh") {
		common.ApiErrorT(c, "Cursor status requires one native account per channel")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	select {
	case cursorStatusReads <- struct{}{}:
		defer func() { <-cursorStatusReads }()
	case <-ctx.Done():
		common.ApiErrorT(c, "Cursor quota is unavailable")
		return
	}
	quota, health, err := service.FetchCursorChannelStatus(ctx, channel)
	status := CursorRouteStatus{Route: strconv.Itoa(id), Quota: quota, Health: health}
	if err != nil {
		// Never include a transport error, account key, or upstream response body.
		status.Error = "Cursor quota is unavailable"
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": status})
}
