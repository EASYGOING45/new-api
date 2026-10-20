package controller

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	nativecursor "github.com/QuantumNous/new-api/pkg/cursor"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorAccountStatusDatabaseMatrix(t *testing.T) {
	for _, dialect := range []struct{ name, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.name, func(t *testing.T) {
			dsn := os.Getenv(dialect.env)
			if dialect.env != "" && dsn == "" {
				t.Skipf("%s not configured", dialect.env)
			}
			db := setupCursorRelayTest(t, dialect.name, dsn)
			versionQuery := "SELECT VERSION()"
			if dialect.name == "sqlite" {
				versionQuery = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
			t.Logf("Database version: %s", version)
			t.Setenv("CURSOR_MAX_IN_FLIGHT", "4")
			upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") == "Bearer fixture-failed" {
					w.WriteHeader(http.StatusUnauthorized)
					io.WriteString(w, "private upstream body fixture-failed")
					return
				}
				switch r.URL.Path {
				case "/auth/exchange_user_api_key":
					io.WriteString(w, `{"accessToken":"fixture-token"}`)
				case "/aiserver.v1.ServerConfigService/GetServerConfig":
					io.WriteString(w, `{}`)
				case "/aiserver.v1.DashboardService/GetCurrentPeriodUsage":
					io.WriteString(w, `{"planUsage":{"autoPercentUsed":20,"apiPercentUsed":100}}`)
				case "/aiserver.v1.DashboardService/GetHardLimit":
					io.WriteString(w, `{}`)
				default:
					t.Errorf("unexpected upstream path: %s", r.URL.Path)
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
			rows := []model.Channel{
				{Id: 11, Type: constant.ChannelTypeCursor, Key: "fixture-healthy", Name: "enabled", Status: common.ChannelStatusEnabled},
				{Id: 12, Type: constant.ChannelTypeCursor, Key: "fixture-failed", Name: "failed", Status: common.ChannelStatusEnabled},
				{Id: 13, Type: constant.ChannelTypeCursor, Key: "fixture-healthy", Name: "disabled alias", Status: common.ChannelStatusManuallyDisabled},
				{Id: 14, Type: constant.ChannelTypeCursor, Key: "first\nsecond", Name: "invalid multi account"},
				{Id: 15, Type: constant.ChannelTypeOpenAI, Key: "other-provider", Name: "not Cursor"},
			}
			require.NoError(t, db.Create(&rows).Error)
			t.Run("upstream routing-name fallback retains the fork account policy", func(t *testing.T) {
				for _, id := range []int{11, 12} {
					priority := int64(0)
					if id == 11 {
						priority = 10
					}
					require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "qwen3-max", ChannelId: id, Enabled: true, Priority: &priority}).Error)
				}
				policy := func(channels []*model.Channel) []*model.Channel {
					result := make([]*model.Channel, 0, len(channels))
					for _, channel := range channels {
						if channel.Id != 11 {
							result = append(result, channel)
						}
					}
					return result
				}
				selected, err := model.GetChannel("default", "qwen3-max@thinking:on", 0, nil, policy)
				require.NoError(t, err)
				require.NotNil(t, selected)
				assert.Equal(t, 12, selected.Id)
			})
			for _, tc := range []struct {
				name         string
				id           int
				valid, quota bool
			}{
				{"enabled account reports both quota windows", 11, true, true},
				{"upstream failure reports unknown without credentials", 12, true, false},
				{"disabled channel still exposes read-only account status", 13, true, true},
				{"multi-key configuration is rejected", 14, false, false},
				{"another provider is rejected", 15, false, false},
				{"unknown channel is rejected", 99, false, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					recorder := httptest.NewRecorder()
					ctx, _ := gin.CreateTestContext(recorder)
					ctx.Params = gin.Params{{Key: "id", Value: strconv.Itoa(tc.id)}}
					ctx.Request = httptest.NewRequest(http.MethodGet, "/", nil)
					GetCursorChannelStatus(ctx)
					var result struct {
						Success bool              `json:"success"`
						Data    CursorRouteStatus `json:"data"`
					}
					require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
					assert.Equal(t, tc.valid, result.Success)
					assert.NotContains(t, recorder.Body.String(), "fixture-")
					assert.NotContains(t, recorder.Body.String(), "private upstream body")
					assert.NotContains(t, recorder.Body.String(), `"key"`)
					if !tc.valid {
						return
					}
					assert.Equal(t, 4, result.Data.Health.MaxInFlight)
					if tc.quota {
						require.NotNil(t, result.Data.Quota)
						assert.Equal(t, []nativecursor.QuotaWindow{{Name: "Cursor Models", UsedPercent: 20}, {Name: "Other Models", UsedPercent: 100}}, result.Data.Quota.Windows)
						assert.Empty(t, result.Data.Error)
					} else {
						assert.Nil(t, result.Data.Quota)
						assert.Equal(t, "Cursor quota is unavailable", result.Data.Error)
					}
				})
			}
		})
	}
}
