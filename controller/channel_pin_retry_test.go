package controller

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	nativecursor "github.com/QuantumNous/new-api/pkg/cursor"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/perf_metrics_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Exercise the real distributor, retry loop and native Cursor adaptor. Only
// Cursor's HTTPS transport is replaced; neither fixture key leaves this test.
func TestNativeCursorChannelFailover(t *testing.T) {
	db := setupCursorRelayTest(t, "sqlite", "")
	channels := []model.Channel{
		{Id: 1, Name: "cursor-fixture-a", Type: constant.ChannelTypeCursor, Key: "fixture-a", Status: common.ChannelStatusEnabled, Models: "grok", Group: "default", Priority: common.GetPointer(int64(10)), AutoBan: common.GetPointer(0)},
		{Id: 2, Name: "cursor-fixture-b", Type: constant.ChannelTypeCursor, Key: "fixture-b", Status: common.ChannelStatusEnabled, Models: "grok", Group: "default", Priority: common.GetPointer(int64(0)), AutoBan: common.GetPointer(0)},
	}
	for i := range channels {
		require.NoError(t, db.Create(&channels[i]).Error)
		require.NoError(t, channels[i].AddAbilities(db))
	}
	field := func(number uint64, data []byte) []byte {
		b := binary.AppendUvarint(nil, number<<3|2)
		b = binary.AppendUvarint(b, uint64(len(data)))
		return append(b, data...)
	}
	frame := func(flags byte, data []byte) []byte {
		b := make([]byte, 5)
		b[0] = flags
		binary.BigEndian.PutUint32(b[1:], uint32(len(data)))
		return append(b, data...)
	}
	for _, tc := range []struct {
		name, mode                          string
		repeats, status, runsA, runsB, used int
		stream, pinned                      bool
	}{
		{name: "HTTP 429 switches and cooldown skips upstream", mode: "rate", repeats: 2, status: 200, runsA: 1, runsB: 2, used: 2},
		{name: "Connect resource exhaustion switches", mode: "connect", repeats: 2, status: 200, runsA: 1, runsB: 2, used: 2},
		{name: "two service errors open circuit", mode: "server", repeats: 3, status: 200, runsA: 2, runsB: 3, used: 2},
		{name: "401 disables only failing key", mode: "auth", repeats: 2, status: 200, runsA: 1, runsB: 2, used: 2},
		{name: "transport failure switches", mode: "transport", repeats: 1, status: 200, runsA: 1, runsB: 1, used: 2},
		{name: "all unavailable respects attempt budget", mode: "all", repeats: 1, status: 429, runsA: 1, runsB: 1, used: 2},
		{name: "504 never retries", mode: "timeout", repeats: 1, status: 504, runsA: 1, used: 1},
		{name: "started stream is not replayed", mode: "broken", repeats: 1, status: 200, runsA: 1, used: 1, stream: true},
		{name: "thinking only upstream completion is not replayed", mode: "empty-stream", repeats: 1, status: 200, runsA: 1, used: 1, stream: true},
		{name: "fixed channel never switches", mode: "rate", repeats: 1, status: 429, runsA: 1, used: 1, pinned: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var runsA, runsB atomic.Int32
			upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/auth/exchange_user_api_key":
					token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
					assert.Contains(t, []string{"fixture-a", "fixture-b"}, token)
					payload, err := common.Marshal(map[string]string{"accessToken": token})
					if assert.NoError(t, err) {
						w.Write(payload)
					}
				case "/aiserver.v1.ServerConfigService/GetServerConfig":
					io.WriteString(w, `{}`)
				case "/aiserver.v1.AiService/AvailableModels":
					io.WriteString(w, `{"models":[{"name":"grok","variants":[{"legacySlug":"grok"}]}]}`)
				case "/agent.v1.AgentService/GetUsableModels":
					io.WriteString(w, `{"models":[{"modelId":"grok"}]}`)
				case "/agent.v1.AgentService/Run":
					assert.Equal(t, 2, r.ProtoMajor)
					var header [5]byte
					if _, err := io.ReadFull(r.Body, header[:]); !assert.NoError(t, err) {
						return
					}
					_, err := io.CopyN(io.Discard, r.Body, int64(binary.BigEndian.Uint32(header[1:])))
					if !assert.NoError(t, err) {
						return
					}
					primary := r.Header.Get("Authorization") == "Bearer fixture-a"
					if primary {
						runsA.Add(1)
					} else {
						assert.Equal(t, "Bearer fixture-b", r.Header.Get("Authorization"))
						runsB.Add(1)
					}
					if primary || tc.mode == "all" {
						switch tc.mode {
						case "rate", "all", "server", "auth", "timeout":
							status := map[string]int{"rate": 429, "all": 429, "server": 503, "auth": 401, "timeout": 504}[tc.mode]
							w.Header().Set("Retry-After", "600")
							w.WriteHeader(status)
							io.WriteString(w, `{}`)
							return
						case "connect":
							w.Header().Set("Content-Type", "application/connect+proto")
							w.Write(frame(2, []byte(`{"error":{"code":"resource_exhausted"}}`)))
							return
						case "transport":
							panic(http.ErrAbortHandler)
						}
					}
					w.Header().Set("Content-Type", "application/connect+proto")
					if primary && tc.mode == "empty-stream" {
						w.Write(frame(0, field(1, field(4, field(1, []byte("synthetic reasoning"))))))
						w.(http.Flusher).Flush()
						w.Write(frame(0, field(1, field(14, []byte{8, 10, 16, 2}))))
						return
					}
					w.Write(frame(0, field(1, field(1, field(1, []byte("fixture answer"))))))
					w.(http.Flusher).Flush()
					if primary && tc.mode == "broken" {
						return
					}
					w.Write(frame(0, field(1, field(14, []byte{8, 10, 16, 2}))))
				default:
					t.Errorf("unexpected fixture endpoint: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			upstream.EnableHTTP2 = true
			upstream.StartTLS()
			t.Cleanup(upstream.Close)
			transport := upstream.Client().Transport.(*http.Transport).Clone()
			transport.TLSClientConfig.ServerName = "example.com"
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			}
			previousTransport, previousClient := http.DefaultTransport, nativecursor.Default
			http.DefaultTransport = transport
			nativecursor.Default = nativecursor.NewClient()
			http.DefaultTransport = previousTransport
			t.Cleanup(func() { nativecursor.Default = previousClient })
			for attempt := range tc.repeats {
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"grok","stream":%t,"messages":[{"role":"user","content":"hello"}]}`, tc.stream)))
				c.Request.Header.Set("Content-Type", "application/json")
				common.SetContextKey(c, constant.ContextKeyUserId, 1)
				common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
				common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
				common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
				common.SetContextKey(c, constant.ContextKeyRequestStartTime, time.Now())
				if tc.pinned {
					service.GetChannelConstraints(c).AddPin(dto.ChannelPin{ChannelId: 1, Source: dto.PinSourceToken, Rank: dto.PinRankToken, RetryMode: dto.PinRetrySingleAttempt})
				}
				middleware.Distribute()(c)
				require.False(t, c.IsAborted(), recorder.Body.String())
				Relay(c, types.RelayFormatOpenAI)
				require.Equal(t, tc.status, recorder.Code, recorder.Body.String())
				used := tc.used
				first := "1"
				if (tc.mode == "rate" || tc.mode == "connect" || tc.mode == "auth") && attempt > 0 || tc.mode == "server" && attempt > 1 {
					used, first = 1, "2"
				}
				require.Len(t, c.GetStringSlice("use_channel"), used)
				assert.Equal(t, first, c.GetStringSlice("use_channel")[0])
				if used == 2 {
					assert.Equal(t, "2", c.GetStringSlice("use_channel")[1])
				}
				if tc.mode == "broken" {
					assert.Contains(t, recorder.Body.String(), "incomplete_stream")
				} else if tc.mode == "empty-stream" {
					assert.Contains(t, recorder.Body.String(), "cursor_empty_response")
					assert.NotContains(t, recorder.Body.String(), "incomplete_stream")
					assert.NotContains(t, recorder.Body.String(), `"finish_reason":"stop"`)
					assert.NotContains(t, recorder.Body.String(), `"content":"synthetic reasoning"`)
				} else if tc.status == 200 {
					assert.Contains(t, recorder.Body.String(), "fixture answer")
				}
				if tc.mode == "all" {
					assert.True(t, slices.ContainsFunc(service.RequestPolicy(c).Events(), func(e service.PolicyEvent) bool { return e.Decision.Reason == "attempt_budget_exhausted" }))
				}
				common.CleanupBodyStorage(c)
			}
			assert.EqualValues(t, tc.runsA, runsA.Load())
			assert.EqualValues(t, tc.runsB, runsB.Load())
		})
	}
}

func TestShouldRetryHonorsPinRetryMode(t *testing.T) {
	openaiErr := types.NewOpenAIError(errors.New("upstream"), types.ErrorCodeBadResponseStatusCode, http.StatusInternalServerError)

	c := newPinRetryContext()
	assert.True(t, service.ShouldRetryRelayError(c, openaiErr, 1))

	origin := newPinRetryContext()
	service.GetChannelConstraints(origin).AddPin(dto.ChannelPin{
		ChannelId: 2,
		Source:    dto.PinSourceOriginTask,
		Rank:      dto.PinRankOriginTask,
		RetryMode: dto.PinRetrySameChannel,
	})
	assert.True(t, service.ShouldRetryRelayError(origin, openaiErr, 1), "origin pin retries on the same channel")

	token := newPinRetryContext()
	service.GetChannelConstraints(token).AddPin(dto.ChannelPin{
		ChannelId: 1,
		Source:    dto.PinSourceToken,
		Rank:      dto.PinRankToken,
		RetryMode: dto.PinRetrySingleAttempt,
	})
	assert.Equal(t, service.PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}, service.DecideRelayRetry(token, openaiErr, 1), "token pin suppresses retry")
}

func TestShouldRetryTaskRelayHonorsPinRetryMode(t *testing.T) {
	taskErr := &dto.TaskError{StatusCode: http.StatusInternalServerError}

	c := newPinRetryContext()
	assert.Equal(t, "retry", decideTaskRetry(c, taskErr, 1).Action)

	origin := newPinRetryContext()
	service.GetChannelConstraints(origin).AddPin(dto.ChannelPin{
		ChannelId: 2,
		Source:    dto.PinSourceOriginTask,
		Rank:      dto.PinRankOriginTask,
		RetryMode: dto.PinRetrySameChannel,
	})
	assert.Equal(t, "retry", decideTaskRetry(origin, taskErr, 1).Action)

	token := newPinRetryContext()
	service.GetChannelConstraints(token).AddPin(dto.ChannelPin{
		ChannelId: 1,
		Source:    dto.PinSourceToken,
		Rank:      dto.PinRankToken,
		RetryMode: dto.PinRetrySingleAttempt,
	})
	assert.Equal(t, service.PolicyDecision{Action: "stop", Reason: "pinned_channel", Source: "channel_constraint"}, decideTaskRetry(token, taskErr, 1))
}

func TestSameChannelPinsMergeToStricterRetryMode(t *testing.T) {
	c := newPinRetryContext()
	constraints := service.GetChannelConstraints(c)
	constraints.AddPin(dto.ChannelPin{
		ChannelId: 7,
		Source:    dto.PinSourceOriginTask,
		Rank:      dto.PinRankOriginTask,
		RetryMode: dto.PinRetrySameChannel,
	})
	constraints.AddPin(dto.ChannelPin{
		ChannelId: 7,
		Source:    dto.PinSourceToken,
		Rank:      dto.PinRankToken,
		RetryMode: dto.PinRetrySingleAttempt,
	})
	pin, found, overridden := constraints.ResolvedPin()
	require.True(t, found)
	assert.Equal(t, 7, pin.ChannelId)
	assert.Equal(t, dto.PinRetrySingleAttempt, pin.RetryMode)
	assert.Empty(t, overridden)
	assert.False(t, service.ShouldRetryRelayError(c, types.NewOpenAIError(errors.New("upstream"), types.ErrorCodeBadResponseStatusCode, http.StatusInternalServerError), 1))
}

func newPinRetryContext() *gin.Context {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return c
}

func TestRequestPolicyConfigReturnsSettingsWithoutMigration(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	GetRequestPolicy(ctx)
	assert.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.True(t, response.Success)
	assert.Contains(t, response.Data, "options")
	assert.NotContains(t, response.Data, "migration")
	assert.NotContains(t, response.Data, "differences")
}

func TestRequestPolicyRoutingDatabaseMatrix(t *testing.T) {
	require.NoError(t, i18n.Init())
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			dsn := ""
			if dialect.env != "" {
				dsn = os.Getenv(dialect.env)
				if dsn == "" {
					t.Skipf("%s not configured", dialect.env)
				}
			}
			db := modelManagementDB(t, dialect.kind, dsn)
			previousGroups := setting.UserUsableGroups2JSONString()
			previousRatios, err := common.Marshal(ratio_setting.GetGroupRatioCopy())
			require.NoError(t, err)
			require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default"}`))
			require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
			t.Cleanup(func() {
				require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousGroups))
				require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(string(previousRatios)))
			})
			// Channel 1 holds the session binding but is no longer usable.
			channels := []model.Channel{
				{Id: 1, Name: "A", Type: 1, Key: "test-only", Status: common.ChannelStatusAutoDisabled, Models: "policy-test", Group: "default", Priority: common.GetPointer(int64(10))},
				{Id: 2, Name: "B", Type: 1, Key: "test-only", Status: common.ChannelStatusEnabled, Models: "policy-test", Group: "default", Priority: common.GetPointer(int64(5))},
			}
			for i := range channels {
				require.NoError(t, db.Create(&channels[i]).Error)
				require.NoError(t, channels[i].AddAbilities(db))
			}
			affinity := operation_setting.GetChannelAffinitySetting()
			previousAffinity := *affinity
			t.Cleanup(func() { *affinity = previousAffinity })
			for _, cached := range []bool{false, true} {
				for _, keep := range []bool{false, true} {
					for _, tc := range []struct {
						globalMode, ruleMode string
						blocked              bool
					}{
						{"strict", "inherit", true},
						{"prefer", "strict", true},
						{"prefer", "inherit", false},
						{"strict", "prefer", false},
					} {
						t.Run(fmt.Sprintf("cache=%t/keep=%t/global=%s/rule=%s", cached, keep, tc.globalMode, tc.ruleMode), func(t *testing.T) {
							common.MemoryCacheEnabled = cached
							model.InitChannelCache()
							snapshot, err := model.BuildRequestPolicy(map[string]string{
								"channel_affinity_setting.enabled":                  "true",
								"channel_affinity_setting.session_mode":             tc.globalMode,
								"channel_affinity_setting.keep_on_channel_disabled": fmt.Sprint(keep),
								"channel_affinity_setting.rules":                    fmt.Sprintf(`[{"name":"session","model_regex":[".*"],"key_sources":[{"type":"request_header","key":"X-Session"}],"session_mode":%q}]`, tc.ruleMode),
							})
							require.NoError(t, err)
							*affinity = snapshot.Affinity
							seed := newPinRetryContext()
							seed.Request.Header.Set("X-Session", t.Name())
							_, found := service.GetPreferredChannelByAffinity(seed, "policy-test", "default")
							require.False(t, found)
							seed.Set("channel_id", 1)
							service.RecordChannelAffinity(seed, 1)
							t.Cleanup(func() { service.ClearCurrentChannelAffinityCache(seed) })
							bound, found := service.GetPreferredChannelByAffinity(seed, "policy-test", "default")
							require.True(t, found)
							require.Equal(t, 1, bound)

							request := newPinRetryContext()
							request.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"policy-test"}`))
							request.Request.Header.Set("Content-Type", "application/json")
							request.Request.Header.Set("X-Session", t.Name())
							common.SetContextKey(request, constant.ContextKeyUsingGroup, "default")
							middleware.Distribute()(request)
							events := service.RequestPolicy(request).Events()
							assert.True(t, slices.ContainsFunc(events, func(event service.PolicyEvent) bool { return event.Decision.Reason == "session_rule_matched" }), "decision events are recorded for every request")
							bound, found = service.GetPreferredChannelByAffinity(seed, "policy-test", "default")
							if tc.blocked {
								assert.Equal(t, http.StatusServiceUnavailable, request.Writer.Status())
								assert.True(t, request.IsAborted())
								assert.Equal(t, keep, found, "binding retention is independent of strict request handling")
								if found {
									assert.Equal(t, 1, bound)
								}
								return
							}
							assert.False(t, request.IsAborted())
							assert.Equal(t, 2, common.GetContextKeyInt(request, constant.ContextKeyChannelId), "prefer falls back to the next eligible channel")
							require.True(t, found)
							assert.Equal(t, 2, bound, "a successful fallback rebinds the session")
						})
					}
				}
			}
		})
	}
}

func setupCursorRelayTest(t *testing.T, kind, dsn string) *gorm.DB {
	t.Helper()
	require.NoError(t, i18n.Init())
	db := modelManagementDB(t, kind, dsn)
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "cursor-fixture", Group: "default"}).Error)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"grok":0,"composer":0,"claude":0,"alias":0}`))
	previousRetry, previousCount := common.RetryTimes, constant.CountToken
	previousTimeout := constant.StreamingTimeout
	previousLog, previousErrorLog := common.LogConsumeEnabled, constant.ErrorLogEnabled
	previousAffinity := *operation_setting.GetChannelAffinitySetting()
	previousRanges := operation_setting.AutomaticRetryStatusCodesToString()
	previousFree := operation_setting.GetQuotaSetting().EnableFreeModelPreConsume
	previousMetrics := perf_metrics_setting.GetSetting().Enabled
	common.RetryTimes, constant.CountToken = 1, false
	constant.StreamingTimeout = 30
	common.LogConsumeEnabled, constant.ErrorLogEnabled = false, false
	operation_setting.GetChannelAffinitySetting().Enabled = false
	operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = false
	config.UpdateConfigFromMap(config.GlobalConfig.Get("perf_metrics_setting"), map[string]string{"enabled": "false"})
	require.NoError(t, operation_setting.AutomaticRetryStatusCodesFromString("401,429,500-503"))
	t.Cleanup(func() {
		common.RetryTimes, constant.CountToken = previousRetry, previousCount
		constant.StreamingTimeout = previousTimeout
		common.LogConsumeEnabled, constant.ErrorLogEnabled = previousLog, previousErrorLog
		*operation_setting.GetChannelAffinitySetting() = previousAffinity
		operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = previousFree
		config.UpdateConfigFromMap(config.GlobalConfig.Get("perf_metrics_setting"), map[string]string{"enabled": fmt.Sprint(previousMetrics)})
		require.NoError(t, operation_setting.AutomaticRetryStatusCodesFromString(previousRanges))
	})

	return db
}

// The transport is a local HTTP/2 server with two synthetic accounts. Run the
// real distributor/relay/retry path against each database, with and without cache.
func TestCursorPoolRoutingDatabaseMatrix(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			dsn := os.Getenv(dialect.env)
			if dialect.env != "" && dsn == "" {
				t.Skipf("%s not configured", dialect.env)
			}
			db := setupCursorRelayTest(t, dialect.kind, dsn)
			mapping := `{"alias":"middle","middle":"claude"}`
			channels := []model.Channel{
				{Id: 1, Name: "primary", Type: constant.ChannelTypeCursor, Key: "fixture-a", Status: 1, Models: "grok,composer,claude,alias", Group: "default", Priority: common.GetPointer(int64(10)), ModelMapping: &mapping, AutoBan: common.GetPointer(0)},
				{Id: 2, Name: "backup", Type: constant.ChannelTypeCursor, Key: "fixture-b", Status: 1, Models: "grok,composer,claude,alias", Group: "default", Priority: common.GetPointer(int64(0)), ModelMapping: &mapping, AutoBan: common.GetPointer(0)},
				{Id: 3, Name: "same-key-alias", Type: constant.ChannelTypeCursor, Key: "fixture-a", Status: 1, Models: "grok,composer,claude,alias", Group: "default", Priority: common.GetPointer(int64(5)), ModelMapping: &mapping, AutoBan: common.GetPointer(0)},
				{Id: 4, Name: "unauthorized-group", Type: constant.ChannelTypeCursor, Key: "must-not-run", Status: 1, Models: "grok,composer,claude,alias", Group: "private", Priority: common.GetPointer(int64(100))},
				{Id: 5, Name: "wrong-model", Type: constant.ChannelTypeCursor, Key: "must-not-run", Status: 1, Models: "different-model", Group: "default", Priority: common.GetPointer(int64(100))},
			}
			for i := range channels {
				require.NoError(t, db.Create(&channels[i]).Error)
				require.NoError(t, channels[i].AddAbilities(db))
			}
			for _, cached := range []bool{false, true} {
				t.Run(fmt.Sprintf("cache=%t", cached), func(t *testing.T) {
					common.MemoryCacheEnabled = cached
					model.InitChannelCache()
					var ticks, runsA, runsB, statusA, statusB atomic.Int64
					ticks.Store(time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC).Unix())
					statusA.Store(429)
					statusB.Store(200)
					var hold atomic.Bool
					entered, release := make(chan struct{}, 1), make(chan struct{})
					var releaseOnce sync.Once
					defer releaseOnce.Do(func() { close(release) })
					field := func(n uint64, b []byte) []byte {
						out := binary.AppendUvarint(nil, n<<3|2)
						out = binary.AppendUvarint(out, uint64(len(b)))
						return append(out, b...)
					}
					frame := func(b []byte) []byte {
						out := make([]byte, 5)
						binary.BigEndian.PutUint32(out[1:], uint32(len(b)))
						return append(out, b...)
					}
					upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
						if key != "fixture-a" && key != "fixture-b" {
							t.Errorf("unexpected synthetic account")
							w.WriteHeader(401)
							return
						}
						switch r.URL.Path {
						case "/auth/exchange_user_api_key":
							fmt.Fprintf(w, `{"accessToken":%q}`, key)
						case "/aiserver.v1.ServerConfigService/GetServerConfig":
							io.WriteString(w, `{}`)
						case "/aiserver.v1.AiService/AvailableModels":
							io.WriteString(w, `{"models":[{"name":"grok","vendorName":"cursor","variants":[{"legacySlug":"grok"}]},{"name":"composer","vendorName":"cursor","variants":[{"legacySlug":"composer"}]},{"name":"claude","vendorName":"anthropic","variants":[{"legacySlug":"claude"}]}]}`)
						case "/agent.v1.AgentService/GetUsableModels":
							io.WriteString(w, `{"models":[{"modelId":"grok"},{"modelId":"composer"},{"modelId":"claude"}]}`)
						case "/aiserver.v1.DashboardService/GetCurrentPeriodUsage":
							used := 10
							if key == "fixture-a" {
								used = 100
							}
							fmt.Fprintf(w, `{"planUsage":{"autoPercentUsed":5,"apiPercentUsed":%d}}`, used)
						case "/aiserver.v1.DashboardService/GetHardLimit":
							io.WriteString(w, `{"noUsageBasedAllowed":true}`)
						case "/agent.v1.AgentService/Run":
							assert.Equal(t, 2, r.ProtoMajor)
							var header [5]byte
							if _, err := io.ReadFull(r.Body, header[:]); err != nil {
								t.Error(err)
								return
							}
							if _, err := io.CopyN(io.Discard, r.Body, int64(binary.BigEndian.Uint32(header[1:]))); err != nil {
								t.Error(err)
								return
							}
							status := statusB.Load()
							if key == "fixture-a" {
								runsA.Add(1)
								status = statusA.Load()
								if hold.CompareAndSwap(true, false) {
									entered <- struct{}{}
									select {
									case <-release:
									case <-r.Context().Done():
										return
									}
								}
							} else {
								runsB.Add(1)
							}
							if status != 200 {
								w.Header().Set("Retry-After", "60")
								w.WriteHeader(int(status))
								io.WriteString(w, `{}`)
								return
							}
							w.Header().Set("Content-Type", "application/connect+proto")
							w.Write(frame(field(1, field(1, field(1, []byte("fixture answer"))))))
							w.Write(frame(field(1, field(14, []byte{8, 10, 16, 2}))))
						default:
							t.Errorf("unexpected fixture endpoint: %s", r.URL.Path)
							http.NotFound(w, r)
						}
					}))
					upstream.EnableHTTP2 = true
					upstream.StartTLS()
					defer func() { releaseOnce.Do(func() { close(release) }); upstream.Close() }()
					transport := upstream.Client().Transport.(*http.Transport).Clone()
					transport.TLSClientConfig.ServerName = "example.com"
					transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
						return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
					}
					previousTransport, previousClient := http.DefaultTransport, nativecursor.Default
					http.DefaultTransport = transport
					nativecursor.Default = nativecursor.NewClientWithClock(func() time.Time { return time.Unix(ticks.Load(), 0) })
					http.DefaultTransport = previousTransport
					defer func() { nativecursor.Default = previousClient }()
					for _, key := range []string{"fixture-a", "fixture-b"} {
						_, err := nativecursor.Default.Catalog(context.Background(), key, false)
						require.NoError(t, err)
						_, err = nativecursor.Default.Quota(context.Background(), key)
						require.NoError(t, err)
					}
					request := func(name string) (*gin.Context, *httptest.ResponseRecorder) {
						w := httptest.NewRecorder()
						c, _ := gin.CreateTestContext(w)
						c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, name)))
						c.Request.Header.Set("Content-Type", "application/json")
						common.SetContextKey(c, constant.ContextKeyUserId, 1)
						common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
						common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
						common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
						common.SetContextKey(c, constant.ContextKeyRequestStartTime, time.Now())
						middleware.Distribute()(c)
						if !c.IsAborted() {
							Relay(c, types.RelayFormatOpenAI)
						}
						common.CleanupBodyStorage(c)
						return c, w
					}
					// Within one priority, fresh exhausted paid budget prefers B.
					require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 2).Update("priority", 10).Error)
					require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", 2).Update("priority", 10).Error)
					model.InitChannelCache()
					preferred, _, err := service.CacheGetRandomSatisfiedChannel(&service.RetryParam{Ctx: newPinRetryContext(), TokenGroup: "default", ModelName: "claude"})
					require.NoError(t, err)
					require.NotNil(t, preferred)
					assert.Equal(t, 2, preferred.Id)
					require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 2).Update("priority", 0).Error)
					require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", 2).Update("priority", 0).Error)
					model.InitChannelCache()
					// Included exhaustion is only a preference; higher-priority A is attempted.
					c, w := request("claude")
					require.Equal(t, 200, w.Code, w.Body.String())
					assert.Equal(t, []string{"1", "2"}, c.GetStringSlice("use_channel"))
					assert.EqualValues(t, 1, runsA.Load())
					assert.EqualValues(t, 1, runsB.Load())
					for _, name := range []string{"claude", "alias"} {
						c, w = request(name)
						require.Equal(t, 200, w.Code, w.Body.String())
						assert.Equal(t, []string{"2"}, c.GetStringSlice("use_channel"))
					}
					// Session rules stay authoritative when the bound quota pool cools.
					for _, mode := range []string{"strict", "prefer"} {
						savedAffinity := *operation_setting.GetChannelAffinitySetting()
						snapshot, err := model.BuildRequestPolicy(map[string]string{
							"channel_affinity_setting.enabled": "true", "channel_affinity_setting.session_mode": mode,
							"channel_affinity_setting.rules": `[{"name":"session","model_regex":[".*"],"key_sources":[{"type":"request_header","key":"X-Session"}],"session_mode":"inherit"}]`,
						})
						require.NoError(t, err)
						*operation_setting.GetChannelAffinitySetting() = snapshot.Affinity
						seed := newPinRetryContext()
						seed.Request.Header.Set("X-Session", t.Name()+mode)
						_, found := service.GetPreferredChannelByAffinity(seed, "claude", "default")
						require.False(t, found)
						service.RecordChannelAffinity(seed, 1)
						bound, _, selectErr := service.SelectChannelForRequest(seed, "claude", &service.RetryParam{Ctx: seed, TokenGroup: "default", ModelName: "claude"})
						if mode == "strict" {
							require.NotNil(t, selectErr)
							assert.Equal(t, 503, selectErr.StatusCode)
							assert.Nil(t, bound)
						} else {
							require.Nil(t, selectErr)
							require.NotNil(t, bound)
							assert.Equal(t, 2, bound.Id)
						}
						service.ClearCurrentChannelAffinityCache(seed)
						*operation_setting.GetChannelAffinitySetting() = savedAffinity
					}
					statusA.Store(200)
					for _, name := range []string{"grok", "composer"} {
						c, w = request(name)
						require.Equal(t, 200, w.Code, w.Body.String())
						assert.Equal(t, []string{"1"}, c.GetStringSlice("use_channel"))
					}
					assert.Equal(t, "quota_pool_cooling", nativecursor.Default.Routing("fixture-a", "claude").Blocked)
					// Expire cooldown without sleeps; one live recovery probe occupies A.
					ticks.Add(61)
					hold.Store(true)
					type response struct {
						c *gin.Context
						w *httptest.ResponseRecorder
					}
					done := make(chan response, 1)
					go func() { c, w := request("claude"); done <- response{c, w} }()
					select {
					case <-entered:
					case <-time.After(5 * time.Second):
						t.Fatal("recovery probe did not reach upstream")
					}
					c, w = request("claude")
					require.Equal(t, 200, w.Code, w.Body.String())
					assert.Equal(t, []string{"2"}, c.GetStringSlice("use_channel"))
					releaseOnce.Do(func() { close(release) })
					result := <-done
					require.Equal(t, 200, result.w.Code, result.w.Body.String())
					assert.Equal(t, []string{"1"}, result.c.GetStringSlice("use_channel"))
					health, err := nativecursor.Default.Health("fixture-a")
					require.NoError(t, err)
					assert.Empty(t, health.Cooldowns)
					c, w = request("claude")
					require.Equal(t, 200, w.Code, w.Body.String())
					assert.Equal(t, []string{"1"}, c.GetStringSlice("use_channel"))
					// A single 503 does not open a circuit. Request-local exclusion must skip
					// duplicate channel 3 anyway, even when the retry budget permits repeats.
					statusA.Store(503)
					common.RetryTimes = 3
					c, w = request("grok")
					require.Equal(t, 200, w.Code, w.Body.String())
					assert.Equal(t, []string{"1", "2"}, c.GetStringSlice("use_channel"))
					assert.Empty(t, nativecursor.Default.Routing("fixture-a", "grok").Blocked)
					assert.True(t, slices.ContainsFunc(service.RequestPolicy(c).Events(), func(e service.PolicyEvent) bool {
						return e.ChannelID == 3 && e.Decision.Reason == "account_already_failed"
					}))
					statusB.Store(503)
					c, w = request("grok")
					assert.Equal(t, 503, w.Code)
					assert.Equal(t, []string{"1", "2"}, c.GetStringSlice("use_channel"), "all distinct accounts exhausted without reusing a failed key")
					// Both accounts now receive a global rate limit, so fresh requests send no Run.
					statusA.Store(429)
					statusB.Store(429)
					ticks.Add(61)
					_, _ = request("grok")
					beforeA, beforeB := runsA.Load(), runsB.Load()
					c, w = request("grok")
					assert.Equal(t, 503, w.Code)
					assert.Empty(t, c.GetStringSlice("use_channel"))
					assert.Equal(t, beforeA, runsA.Load())
					assert.Equal(t, beforeB, runsB.Load())
					common.RetryTimes = 1
				})
			}
		})
	}
}
