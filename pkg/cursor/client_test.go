package cursor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorModelParametersReachHTTP2Run(t *testing.T) {
	type capturedModel struct {
		id, detailsID           string
		maxMode, detailsMaxMode uint64
		parameters              map[string]string
	}
	captured := make(chan capturedModel, 1)
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/exchange_user_api_key":
			io.WriteString(w, `{"accessToken":"fixture-token"}`)
		case "/aiserver.v1.ServerConfigService/GetServerConfig":
			io.WriteString(w, `{}`)
		case "/agent.v1.AgentService/Run":
			assert.Equal(t, 2, r.ProtoMajor)
			first, err := readConnectFrame(bufio.NewReader(r.Body))
			if !assert.NoError(t, err) {
				return
			}
			run := pbFields([]byte(pbStr(pbFields(first.data), 1)))
			requested, details := pbFields([]byte(pbStr(run, 9))), pbFields([]byte(pbStr(run, 3)))
			parameters := make(map[string]string)
			for _, item := range requested {
				if item.num == 3 {
					pair := pbFields(item.data)
					parameters[pbStr(pair, 1)] = pbStr(pair, 2)
				}
			}
			captured <- capturedModel{pbStr(requested, 1), pbStr(details, 1), pbNum(requested, 2), pbNum(details, 7), parameters}
			w.Write(connectFrame(pb{}.bytes(1, pb{}.bytes(1, pb{}.str(1, "configured")))))
			w.Write(connectFrame(pb{}.bytes(1, pb{}.bytes(14, pb{}.varint(1, 10).varint(2, 2)))))
		default:
			http.NotFound(w, r)
		}
	})
	for _, effort := range []string{"low", "medium", "high", "xhigh"} {
		for _, fast := range []bool{false, true} {
			for _, contextSize := range []string{"256k", "500k"} {
				t.Run(fmt.Sprintf("%s/fast=%t/context=%s", effort, fast, contextSize), func(t *testing.T) {
					// Exercise the shared DTO serialization too: explicit false must survive.
					var dto kitdto.GeneralOpenAIRequest
					require.NoError(t, common.UnmarshalJsonStr(fmt.Sprintf(`{"model":"grok-4.7","messages":[{"role":"user","content":"hello"}],"reasoning_effort":%q,"cursor":{"fast":%t,"context":%q}}`, effort, fast, contextSize), &dto))
					body, err := common.Marshal(dto)
					require.NoError(t, err)
					res := c.Chat(context.Background(), "fixture-key", body)
					data, err := io.ReadAll(res.Body)
					res.Body.Close()
					require.NoError(t, err)
					require.Equal(t, 200, res.StatusCode, string(data))
					actual := <-captured
					id := "grok-4.7-" + effort
					if fast {
						id += "-fast"
					}
					assert.Equal(t, id, actual.id)
					assert.Equal(t, id, actual.detailsID)
					assert.Equal(t, map[string]string{"context": contextSize, "reasoning_effort": effort, "fast": fmt.Sprint(fast), "priority_tier": "default"}, actual.parameters)
					assert.Equal(t, contextSize == "500k", actual.maxMode == 1)
					assert.Equal(t, actual.maxMode, actual.detailsMaxMode)
				})
			}
		}
	}
}

func TestCursorModelVariantsAndValidation(t *testing.T) {
	picker, usable := catalogFixture(t)
	catalog, err := parseCatalog(picker, usable, time.Now())
	require.NoError(t, err)
	for _, id := range catalog.IDs() {
		r, _, _, _, err := parseRequest([]byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, id)))
		require.NoError(t, err)
		selected, err := resolveModel(catalog, r)
		require.NoError(t, err)
		require.NotEmpty(t, selected.ID)
	}
	for _, tc := range []struct{ model, extra string }{
		{"grok-4.7", `"reasoning_effort":"ultra"`},
		{"grok-4.7", `"reasoning_effort":5`},
		{"grok-4.7", `"cursor":{"fast":"false"}`},
		{"grok-4.7", `"cursor":{"context":"1m"}`},
		{"grok-4.7", `"cursor":{"context":256000}`},
		{"grok-4.7", `"cursor":{"unknown":true}`},
		{"grok-4.7-high-fast", `"reasoning_effort":"low"`},
		{"grok-4.7-high-fast", `"cursor":{"fast":false}`},
		{"grok-4.7", `"reasoning_effort":"low","cursor":{"parameters":{"reasoning_effort":"high"}}`},
		{"composer-2.5", `"reasoning_effort":"high"`},
		{"not-in-account", `"cursor":{}`},
	} {
		r, _, _, _, err := parseRequest([]byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],%s}`, tc.model, tc.extra)))
		if err == nil {
			_, err = resolveModel(catalog, r)
		}
		require.Error(t, err, "%+v", tc)
	}
	for _, tc := range []struct {
		model, extra, effortKey, effort, context string
		fast                                     bool
	}{
		{"grok-4.7", `"cursor":{}`, "reasoning_effort", "high", "256k", true},
		{"claude-opus-5-5", `"reasoning_effort":"max","cursor":{"fast":false,"context":"1m"}`, "effort", "max", "1m", false},
		{"gpt-5.5", `"cursor":{"parameters":{"reasoning":"extra-high","fast":"false","context":"1m"}}`, "reasoning", "extra-high", "1m", false},
	} {
		r, _, _, _, err := parseRequest([]byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],%s}`, tc.model, tc.extra)))
		require.NoError(t, err)
		selected, err := resolveModel(catalog, r)
		require.NoError(t, err, "%+v", tc)
		assert.Equal(t, tc.effort, selected.Parameters[tc.effortKey])
		assert.Equal(t, tc.context, selected.Parameters["context"])
		assert.Equal(t, fmt.Sprint(tc.fast), selected.Parameters["fast"])
	}
}

func catalogFixture(t *testing.T) ([]byte, []byte) {
	t.Helper()
	picker, err := os.ReadFile("testdata/catalog-picker.json")
	require.NoError(t, err)
	usable, err := os.ReadFile("testdata/catalog-usable.json")
	require.NoError(t, err)
	return picker, usable
}

type fixtureTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	u := *r.URL
	u.Scheme = f.target.Scheme
	u.Host = f.target.Host
	r.URL = &u
	return f.base.RoundTrip(r)
}
func fixtureClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	picker, usable := catalogFixture(t)
	return catalogFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/aiserver.v1.AiService/AvailableModels":
			w.Write(picker)
		case "/agent.v1.AgentService/GetUsableModels":
			w.Write(usable)
		default:
			handler(w, r)
		}
	})
}
func catalogFixtureClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewUnstartedServer(handler)
	s.EnableHTTP2 = true
	s.StartTLS()
	t.Cleanup(s.Close)
	u, err := url.Parse(s.URL)
	require.NoError(t, err)
	c := NewClient()
	c.http = s.Client()
	c.http.Transport = fixtureTransport{c.http.Transport, u}
	return c
}

func TestNativeHTTP2Chat(t *testing.T) {
	for _, tc := range []struct {
		name                                                                                  string
		stream, broken, tool, invalidUsage, thinkingOnly, connectEnd, empty, thinkingThenText bool
	}{{name: "text"}, {name: "stream", stream: true}, {name: "broken stream", stream: true, broken: true}, {name: "tool", stream: true, tool: true}, {name: "invalid usage", stream: true, invalidUsage: true}, {name: "thinking only turn end", stream: true, thinkingOnly: true}, {name: "thinking only connect end", stream: true, thinkingOnly: true, connectEnd: true}, {name: "thinking only nonstream", thinkingOnly: true}, {name: "empty turn end", empty: true}, {name: "thinking then text", stream: true, thinkingThenText: true}} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/auth/exchange_user_api_key":
					io.WriteString(w, `{"accessToken":"fixture-token"}`)
				case "/aiserver.v1.ServerConfigService/GetServerConfig":
					io.WriteString(w, `{"agentUrlConfig":{"agentUrl":"https://agent.test.cursor.sh"}}`)
				case "/agent.v1.AgentService/Run":
					assert.Equal(t, 2, r.ProtoMajor)
					assert.Equal(t, "Bearer fixture-token", r.Header.Get("Authorization"))
					assert.True(t, strings.HasPrefix(r.Header.Get("x-cursor-client-version"), "cli-"))
					reader := bufio.NewReader(r.Body)
					first, err := readConnectFrame(reader)
					if !assert.NoError(t, err) {
						return
					}
					run := pbFields([]byte(pbStr(pbFields(first.data), 1)))
					state := pbFields([]byte(pbStr(run, 1)))
					var hash []byte
					for _, f := range state {
						if f.num == 1 {
							hash = f.data
						}
					}
					w.Header().Set("Content-Type", "application/connect+proto")
					w.Write(connectFrame(pb{}.bytes(4, pb{}.varint(1, 7).bytes(2, pb{}.bytes(1, hash)))))
					w.(http.Flusher).Flush()
					reply, err := readConnectFrame(reader)
					if !assert.NoError(t, err) {
						return
					}
					assert.Contains(t, string(reply.data), "hello") // Actual bidirectional blob delivery.
					if tc.tool {
						w.Write(connectFrame(pb{}.bytes(1, pb{}.bytes(27, pb{}.varint(1, 1)))))
						args := pb{}.str(1, "magpie-lookup").str(3, "call_1\nfc_2").str(5, "lookup").bytes(2, pb{}.str(1, "label").bytes(2, pbValue("local")))
						w.Write(connectFrame(pb{}.bytes(2, pb{}.varint(1, 1).bytes(11, args))))
						return
					}
					field := 1
					if tc.thinkingOnly {
						field = 4
					}
					// Captured Composer responses contain heartbeat/step/checkpoint messages.
					// Step 1 is a step ID, not an error or a token-limit finish reason.
					w.Write(connectFrame(pb{}.bytes(1, pb{}.bytes(13, nil))))
					if tc.thinkingThenText {
						w.Write(connectFrame(pb{}.bytes(1, pb{}.bytes(4, pb{}.str(1, "Let me think.")))))
					}
					if !tc.empty {
						w.Write(connectFrame(pb{}.bytes(1, pb{}.bytes(field, pb{}.str(1, "42")))))
					}
					w.Write(connectFrame(pb{}.bytes(1, pb{}.bytes(17, pb{}.varint(1, 1).varint(2, 22072)))))
					if tc.thinkingOnly {
						w.Write(connectFrame(pb{}.bytes(3, pb{}.str(4, `{"role":"assistant","content":[{"type":"reasoning","text":"synthetic thought only"}]}`))))
					}
					w.(http.Flusher).Flush()
					if tc.connectEnd {
						end := connectFrame([]byte(`{}`))
						end[0] = 2
						w.Write(end)
						return
					}
					if tc.broken {
						return
					}
					usage := pb{}.varint(1, 10).varint(2, 2).varint(3, 3)
					if tc.invalidUsage {
						usage = pb{}.varint(1, ^uint64(0))
					}
					w.Write(connectFrame(pb{}.bytes(1, pb{}.bytes(14, usage))))
				default:
					http.NotFound(w, r)
				}
			})
			raw := map[string]any{"model": "grok", "stream": tc.stream, "messages": []any{map[string]any{"role": "user", "content": "hello"}}}
			if tc.tool {
				raw["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object"}}}}
			}
			b, err := common.Marshal(raw)
			require.NoError(t, err)
			res := c.Chat(context.Background(), "fixture-key", b)
			data, err := io.ReadAll(res.Body)
			res.Body.Close()
			require.NoError(t, err)
			if tc.empty {
				require.Equal(t, 502, res.StatusCode)
			} else {
				require.Equal(t, 200, res.StatusCode)
			}
			text := string(data)
			if tc.broken || tc.invalidUsage || tc.thinkingOnly || tc.empty {
				assert.Contains(t, text, `"error"`)
				assert.NotContains(t, text, "[DONE]")
				assert.NotContains(t, text, `"finish_reason":"stop"`)
				if tc.thinkingOnly || tc.empty {
					assert.Contains(t, text, `"code":"cursor_empty_response"`)
					assert.NotContains(t, text, `"content":"42"`)
					require.Eventually(t, func() bool { h, _ := c.Health("fixture-key"); return h.InFlight == 0 }, time.Second, time.Millisecond)
					h, err := c.Health("fixture-key")
					require.NoError(t, err)
					require.Len(t, h.LastRequests, 1)
					assert.Equal(t, 502, h.LastRequests[0].Status)
				}
				if tc.broken {
					assert.Contains(t, text, `"code":"incomplete_stream"`)
				}
				return
			}
			if tc.stream {
				assert.Contains(t, text, "[DONE]")
			}
			if tc.tool {
				assert.Contains(t, text, "call_1__fc_2")
				assert.Contains(t, text, "tool_calls")
				assert.Contains(t, text, "local")
				assert.NotContains(t, text, "prompt_tokens")
			} else {
				assert.Contains(t, text, "42")
				assert.Contains(t, text, `"prompt_tokens":10`)
				assert.Contains(t, text, `"cached_tokens":3`)
			}
		})
	}
}

func TestAuthenticationAndEndpointIsolation(t *testing.T) {
	for _, tc := range []struct {
		name, agent      string
		authStatus, want int
	}{{"bad key", "", 401, 401}, {"foreign endpoint", "https://attacker.example", 200, 502}, {"insecure endpoint", "http://agent.test.cursor.sh", 200, 502}, {"credential URL", "https://user@agent.test.cursor.sh", 200, 502}} {
		t.Run(tc.name, func(t *testing.T) {
			var runs atomic.Int32
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/auth/exchange_user_api_key":
					w.WriteHeader(tc.authStatus)
					io.WriteString(w, `{"accessToken":"fixture-token","message":"secret-key"}`)
				case "/aiserver.v1.ServerConfigService/GetServerConfig":
					b, _ := common.Marshal(map[string]any{"agentUrlConfig": map[string]any{"agentUrl": tc.agent}})
					w.Write(b)
				default:
					runs.Add(1)
					w.WriteHeader(500)
				}
			})
			res := c.Chat(context.Background(), "secret-key", []byte(`{"model":"grok","messages":[{"role":"user","content":"hello"}]}`))
			b, err := io.ReadAll(res.Body)
			res.Body.Close()
			require.NoError(t, err)
			assert.Equal(t, tc.want, res.StatusCode)
			assert.NotContains(t, string(b), "secret-key")
			assert.NotContains(t, string(b), "fixture-token")
			assert.Zero(t, runs.Load())
		})
	}
}

func TestConversationAndInputContracts(t *testing.T) {
	_, messages, _, _, err := parseRequest([]byte(`{"model":"grok","messages":[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"lookup","arguments":"{}"}},{"id":"b","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":"result-a"},{"role":"tool","tool_call_id":"b","content":"result-b"}]}`))
	require.NoError(t, err)
	require.Len(t, messages, 2)
	assert.Contains(t, string(messages[1]), "result-a")
	assert.Contains(t, string(messages[1]), "result-b")
	assert.NotContains(t, string(messages[1]), "interrupted")
	for _, extra := range []string{`,"temperature":0`, `,"max_tokens":0`, `,"n":2`, `,"tool_choice":"required"`} {
		_, _, _, _, err = parseRequest([]byte(`{"model":"grok","messages":[{"role":"user","content":"hello"}]` + extra + `}`))
		require.Error(t, err)
	}
	var header [5]byte
	binary.BigEndian.PutUint32(header[1:], 9<<20)
	_, err = readConnectFrame(bufio.NewReader(bytes.NewReader(header[:])))
	require.Error(t, err)
	_, err = cursorUsage([]pbField{{num: 1, n: 10}, {num: 3, n: 11}})
	require.Error(t, err)
}

func TestAccountHealthAndRecovery(t *testing.T) {
	c := NewClient()
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	a, err := c.account("key-a")
	require.NoError(t, err)
	b, err := c.account("key-b")
	require.NoError(t, err)
	require.Zero(t, c.acquire(a))
	require.Zero(t, c.acquire(a))
	assert.Equal(t, 429, c.acquire(a))
	c.finish(a, 502, 0, false)
	c.finish(a, 502, 0, false)
	assert.Equal(t, 429, c.acquire(a))
	assert.Zero(t, c.acquire(b))
	c.finish(b, 200, 0, false)
	now = now.Add(31 * time.Second)
	require.Zero(t, c.acquire(a))
	assert.Equal(t, 429, c.acquire(a))
	c.finish(a, 200, 0, false)
	require.Zero(t, c.acquire(a))
	c.finish(a, 429, retryDelay("600", now), false)
	now = now.Add(599 * time.Second)
	assert.Equal(t, 429, c.acquire(a))
	now = now.Add(time.Second)
	require.Zero(t, c.acquire(a))
	c.finish(a, 504, 0, true)
	require.Zero(t, c.acquire(a))
	c.finish(a, 401, 0, false)
	assert.Equal(t, 401, c.acquire(a))
	rotated, err := c.account("rotated-key")
	require.NoError(t, err)
	assert.Zero(t, c.acquire(rotated))
	c.finish(rotated, 200, 0, false)
}

func TestConfiguredConcurrency(t *testing.T) {
	for _, tc := range []struct {
		value string
		limit int
	}{{"", 2}, {"invalid", 2}, {"0", 2}, {"-1", 2}, {"65", 2}, {"1", 1}, {" 4 ", 4}, {"8", 8}, {"64", 64}} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("CURSOR_MAX_IN_FLIGHT", tc.value)
			now := time.Unix(1000, 0)
			c := NewClientWithClock(func() time.Time { return now })
			a, err := c.account("key")
			require.NoError(t, err)
			a.catalog = &Catalog{Models: []CatalogModel{{ID: "grok", BudgetScope: CursorModels}, {ID: "claude", BudgetScope: OtherModels}}}
			// Both pools share the account cap, even when individual pools have room.
			for i := range tc.limit {
				require.Zero(t, c.acquire(a))
				scope := CursorModels
				if i%2 != 0 {
					scope = OtherModels
				}
				require.Zero(t, c.acquireWindow(a, scope))
			}
			h, err := c.Health("key")
			require.NoError(t, err)
			assert.Equal(t, tc.limit, h.MaxInFlight)
			assert.Equal(t, tc.limit, h.InFlight)
			assert.Equal(t, 429, c.acquire(a))
			assert.Equal(t, "account_busy", c.Routing("key", "grok").Blocked)
			assert.Equal(t, "account_busy", c.Routing("key", "claude").Blocked)
			for i := range tc.limit {
				scope := CursorModels
				if i%2 != 0 {
					scope = OtherModels
				}
				c.finishRun(context.Background(), "key", a, scope, 200, 0, false)
			}
			// Increasing the normal cap never admits multiple half-open probes.
			require.Zero(t, c.acquire(a))
			c.finish(a, 429, 0, false)
			assert.Equal(t, "account_cooling", c.Routing("key", "grok").Blocked)
			now = now.Add(30 * time.Second)
			require.Zero(t, c.acquire(a))
			require.Equal(t, 429, c.acquire(a))
			c.finish(a, 200, 0, false)
			for range tc.limit {
				require.Zero(t, c.acquire(a))
			}
			for range tc.limit {
				c.finish(a, 200, 0, false)
			}
			assert.Empty(t, c.Routing("key", "grok").Blocked)
		})
	}
}

func TestConfiguredConcurrencyReachesHTTP2Upstream(t *testing.T) {
	for _, limit := range []int{2, 4, 8} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			t.Setenv("CURSOR_MAX_IN_FLIGHT", fmt.Sprint(limit))
			var runs atomic.Int32
			release := make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/auth/exchange_user_api_key":
					io.WriteString(w, `{"accessToken":"fixture-token"}`)
				case "/aiserver.v1.ServerConfigService/GetServerConfig":
					io.WriteString(w, `{}`)
				case "/agent.v1.AgentService/Run":
					assert.Equal(t, 2, r.ProtoMajor)
					_, err := readConnectFrame(bufio.NewReader(r.Body))
					if !assert.NoError(t, err) {
						return
					}
					runs.Add(1)
					w.Header().Set("Content-Type", "application/connect+proto")
					w.Write(connectFrame(pb{}.bytes(1, pb{}.bytes(1, pb{}.str(1, "hello")))))
					w.(http.Flusher).Flush()
					select {
					case <-release:
						w.Write(connectFrame(pb{}.bytes(1, pb{}.bytes(14, pb{}.varint(1, 10).varint(2, 2)))))
					case <-r.Context().Done():
					}
				default:
					http.NotFound(w, r)
				}
			})
			t.Cleanup(cancel)
			body := []byte(`{"model":"grok","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
			responses := make(chan *http.Response, limit+1)
			var wg sync.WaitGroup
			for range limit + 1 {
				wg.Go(func() { responses <- c.Chat(ctx, "key", body) })
			}
			wg.Wait()
			close(responses)
			accepted := make([]*http.Response, 0, limit)
			for res := range responses {
				t.Cleanup(func() { res.Body.Close() })
				if res.StatusCode == 200 {
					accepted = append(accepted, res)
				} else {
					assert.Equal(t, 429, res.StatusCode)
				}
			}
			require.Len(t, accepted, limit)
			assert.EqualValues(t, limit, runs.Load(), "the excess request must not reach the upstream")
			close(release)
			for _, res := range accepted {
				data, err := io.ReadAll(res.Body)
				require.NoError(t, err)
				assert.Contains(t, string(data), "[DONE]")
			}
			require.Eventually(t, func() bool { h, _ := c.Health("key"); return h.InFlight == 0 }, time.Second, time.Millisecond)
			assert.Empty(t, c.Routing("key", "grok").Blocked)
		})
	}
}

func TestCooldownSurvivesConcurrentCompletions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		advance time.Duration
		neutral bool
	}{
		{name: "older success during cooldown", status: 200},
		{name: "older success after cooldown", status: 200, advance: 10 * time.Minute},
		{name: "shorter rate limit cannot reduce Retry-After", status: 429},
		{name: "server error cannot reduce Retry-After", status: 503},
		{name: "cancellation preserves cooldown", status: 504, neutral: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient()
			start := time.Unix(1000, 0)
			now := start
			c.now = func() time.Time { return now }
			a, err := c.account("fixture-account")
			require.NoError(t, err)
			require.Zero(t, c.acquire(a))
			require.Zero(t, c.acquire(a))
			c.finish(a, 429, 10*time.Minute, false)
			now = now.Add(tc.advance)
			c.finish(a, tc.status, 0, tc.neutral)
			if tc.advance < 10*time.Minute {
				now = start.Add(10*time.Minute - time.Second)
				require.Equal(t, 429, c.acquire(a), "the original Retry-After must remain in effect")
			}
			now = start.Add(10 * time.Minute)
			require.Zero(t, c.acquire(a))
			require.Equal(t, 429, c.acquire(a), "only one recovery probe may run")
			c.finish(a, 200, 0, false)
			require.Zero(t, c.acquire(a))
			require.Zero(t, c.acquire(a), "a successful probe restores normal concurrency")
			c.finish(a, 200, 0, false)
			c.finish(a, 200, 0, false)
		})
	}
}

func TestRecoveryProbeFailureAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		neutral bool
	}{
		{name: "failed probe", status: 503},
		{name: "canceled probe", status: 504, neutral: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient()
			now := time.Unix(1000, 0)
			c.now = func() time.Time { return now }
			a, err := c.account("fixture-account")
			require.NoError(t, err)
			require.Zero(t, c.acquire(a))
			c.finish(a, 429, 0, false)
			now = now.Add(30 * time.Second)
			require.Zero(t, c.acquire(a))
			c.finish(a, tc.status, 0, tc.neutral)
			if !tc.neutral {
				require.Equal(t, 429, c.acquire(a))
				now = now.Add(30 * time.Second)
			}
			require.Zero(t, c.acquire(a), "cancellation must release the probe slot")
			require.Equal(t, 429, c.acquire(a))
			c.finish(a, 200, 0, false)
			require.Zero(t, c.acquire(a))
			require.Zero(t, c.acquire(a))
			c.finish(a, 200, 0, false)
			c.finish(a, 200, 0, false)
		})
	}
}

func TestHTTP2ConcurrentCooldownAndRecovery(t *testing.T) {
	var now atomic.Int64
	now.Store(1000)
	var runsA, runsB atomic.Int32
	olderDone, probeDone := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/exchange_user_api_key":
			token := "token-a"
			if r.Header.Get("Authorization") == "Bearer fixture-b" {
				token = "token-b"
			}
			body, err := common.Marshal(map[string]string{"accessToken": token})
			if assert.NoError(t, err) {
				w.Write(body)
			}
		case "/aiserver.v1.ServerConfigService/GetServerConfig":
			io.WriteString(w, `{}`)
		case "/agent.v1.AgentService/Run":
			assert.Equal(t, 2, r.ProtoMajor)
			_, err := readConnectFrame(bufio.NewReader(r.Body))
			if !assert.NoError(t, err) {
				return
			}
			var release <-chan struct{}
			if r.Header.Get("Authorization") == "Bearer token-a" {
				switch runsA.Add(1) {
				case 1:
					release = olderDone
				case 2:
					w.Header().Set("Retry-After", "600")
					w.WriteHeader(http.StatusTooManyRequests)
					io.WriteString(w, `{"error":{"code":"resource_exhausted"}}`)
					return
				case 3:
					release = probeDone
				}
			} else {
				assert.Equal(t, "Bearer token-b", r.Header.Get("Authorization"))
				runsB.Add(1)
			}
			w.Header().Set("Content-Type", "application/connect+proto")
			w.Write(connectFrame(pb{}.bytes(1, pb{}.bytes(1, pb{}.str(1, "healthy")))))
			w.(http.Flusher).Flush()
			if release != nil {
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			w.Write(connectFrame(pb{}.bytes(1, pb{}.bytes(14, pb{}.varint(1, 10).varint(2, 2)))))
		default:
			http.NotFound(w, r)
		}
	})
	// Cancel outstanding HTTP/2 handlers before fixtureClient closes its server.
	t.Cleanup(cancel)
	c.now = func() time.Time { return time.Unix(now.Load(), 0) }
	body := []byte(`{"model":"grok","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	older := c.Chat(ctx, "fixture-a", body)
	require.Equal(t, 200, older.StatusCode)
	t.Cleanup(func() { older.Body.Close() })
	failure := c.Chat(ctx, "fixture-a", body)
	require.Equal(t, 429, failure.StatusCode)
	failure.Body.Close()
	other := c.Chat(ctx, "fixture-b", body)
	require.Equal(t, 200, other.StatusCode)
	otherData, err := io.ReadAll(other.Body)
	other.Body.Close()
	require.NoError(t, err)
	assert.Contains(t, string(otherData), "[DONE]")
	assert.EqualValues(t, 1, runsB.Load(), "a rate-limited account must not block another account")

	close(olderDone)
	olderData, err := io.ReadAll(older.Body)
	older.Body.Close()
	require.NoError(t, err)
	assert.Contains(t, string(olderData), "[DONE]")
	blocked := c.Chat(ctx, "fixture-a", body)
	require.Equal(t, 429, blocked.StatusCode)
	blocked.Body.Close()
	require.EqualValues(t, 2, runsA.Load(), "cooldown must reject locally without another upstream Run")

	now.Store(1600)
	probe := c.Chat(ctx, "fixture-a", body)
	require.Equal(t, 200, probe.StatusCode)
	t.Cleanup(func() { probe.Body.Close() })
	blocked = c.Chat(ctx, "fixture-a", body)
	require.Equal(t, 429, blocked.StatusCode, "a second recovery probe must be rejected")
	blocked.Body.Close()
	require.EqualValues(t, 3, runsA.Load())
	close(probeDone)
	probeData, err := io.ReadAll(probe.Body)
	probe.Body.Close()
	require.NoError(t, err)
	assert.Contains(t, string(probeData), "[DONE]")
	healthy := c.Chat(ctx, "fixture-a", body)
	require.Equal(t, 200, healthy.StatusCode)
	healthyData, err := io.ReadAll(healthy.Body)
	healthy.Body.Close()
	require.NoError(t, err)
	assert.Contains(t, string(healthyData), "[DONE]")
	assert.EqualValues(t, 4, runsA.Load())
}

func TestQuotaCacheExpiryAndUnknownUsage(t *testing.T) {
	var exchanges atomic.Int32
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/exchange_user_api_key":
			exchanges.Add(1)
			io.WriteString(w, `{"accessToken":"fixture-token"}`)
		case "/aiserver.v1.ServerConfigService/GetServerConfig":
			io.WriteString(w, `{}`)
		case "/aiserver.v1.DashboardService/GetCurrentPeriodUsage":
			io.WriteString(w, `{"planUsage":{"autoPercentUsed":12.5},"billingCycleEnd":"1800000000000"}`)
		default:
			http.NotFound(w, r)
		}
	})
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	for range 2 {
		quota, err := c.Quota(context.Background(), "key-a")
		require.NoError(t, err)
		require.Len(t, quota.Windows, 1) // Missing buckets must not appear as 0% used.
		assert.Nil(t, quota.MonetaryBalance)
	}
	assert.EqualValues(t, 1, exchanges.Load())
	now = now.Add(11 * time.Minute)
	_, err := c.Quota(context.Background(), "key-a")
	require.NoError(t, err)
	assert.EqualValues(t, 2, exchanges.Load())
	_, err = c.Quota(context.Background(), "key-b")
	require.NoError(t, err)
	assert.EqualValues(t, 3, exchanges.Load()) // Cached token is never shared across keys.
}

func TestVersionFailureIsNotAccountQuota(t *testing.T) {
	status, message := cursorFailure(200, []byte(`{"error":{"code":"resource_exhausted","message":"Error","details":[{"debug":{"details":{"title":"Update Required"}}}]}}`))
	assert.Equal(t, 400, status)
	assert.Contains(t, message, "version")
}

func TestClosingClientResponseCancelsCursorRun(t *testing.T) {
	stopped := make(chan struct{})
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/exchange_user_api_key":
			io.WriteString(w, `{"accessToken":"fixture-token"}`)
		case "/aiserver.v1.ServerConfigService/GetServerConfig":
			io.WriteString(w, `{}`)
		case "/agent.v1.AgentService/Run":
			_, err := readConnectFrame(bufio.NewReader(r.Body))
			if !assert.NoError(t, err) {
				return
			}
			w.Write(connectFrame(pb{}.bytes(1, pb{}.bytes(1, pb{}.str(1, "partial")))))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			close(stopped)
		}
	})
	res := c.Chat(context.Background(), "key", []byte(`{"model":"grok","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	require.Equal(t, 200, res.StatusCode)
	require.NoError(t, res.Body.Close())
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("closing downstream response did not cancel the upstream Run")
	}
	require.Eventually(t, func() bool { h, _ := c.Health("key"); return h.InFlight == 0 }, time.Second, time.Millisecond)
	h, err := c.Health("key")
	require.NoError(t, err)
	assert.Empty(t, h.LastRequests, "canceling before upstream completion is not a successful observation")
}

func TestDynamicCatalogRefreshAndIsolation(t *testing.T) {
	picker, usable := catalogFixture(t)
	var pickerCalls, usableCalls, mode atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	c := catalogFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/exchange_user_api_key":
			b, _ := common.Marshal(map[string]string{"accessToken": strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")})
			w.Write(b)
		case "/aiserver.v1.ServerConfigService/GetServerConfig":
			io.WriteString(w, `{}`)
		case "/aiserver.v1.AiService/AvailableModels":
			if pickerCalls.Add(1) == 1 {
				close(entered)
				<-release
			}
			w.Write(picker)
		case "/agent.v1.AgentService/GetUsableModels":
			usableCalls.Add(1)
			switch mode.Load() {
			case 1:
				w.WriteHeader(503)
				io.WriteString(w, `{"secret":"must-not-escape"}`)
			case 2:
				io.WriteString(w, `{}`)
			case 3:
				io.WriteString(w, `{"models":[]}`)
			default:
				if r.Header.Get("Authorization") == "Bearer account-b" {
					io.WriteString(w, `{"models":[{"modelId":"grok"}]}`)
				} else {
					w.Write(usable)
				}
			}
		default:
			http.NotFound(w, r)
		}
	})
	var clock atomic.Int64
	clock.Store(time.Now().Unix())
	c.now = func() time.Time { return time.Unix(clock.Load(), 0) }
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			cat, err := c.Catalog(context.Background(), "account-a", false)
			assert.NoError(t, err)
			assert.NotNil(t, cat)
		})
	}
	<-entered
	close(release)
	wg.Wait()
	assert.EqualValues(t, 1, pickerCalls.Load())
	assert.EqualValues(t, 1, usableCalls.Load())
	a, err := c.Catalog(context.Background(), "account-a", false)
	require.NoError(t, err)
	b, err := c.Catalog(context.Background(), "account-b", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"grok"}, b.IDs())
	assert.Greater(t, len(a.Models), 1)
	// Refresh failures must not return a formerly valid catalog or upstream bodies.
	mode.Store(1)
	clock.Add(301)
	stale, err := c.Catalog(context.Background(), "account-a", false)
	require.Error(t, err)
	assert.Nil(t, stale)
	assert.NotContains(t, err.Error(), "must-not-escape")
	before := pickerCalls.Load()
	_, err = c.Catalog(context.Background(), "account-a", true)
	require.Error(t, err)
	assert.Equal(t, before, pickerCalls.Load())
	mode.Store(2)
	clock.Add(11)
	_, err = c.Catalog(context.Background(), "account-a", false)
	require.ErrorContains(t, err, "invalid")
	mode.Store(3)
	clock.Add(11)
	empty, err := c.Catalog(context.Background(), "account-a", false)
	require.NoError(t, err)
	assert.Empty(t, empty.Models)
	mode.Store(0)
	refreshed, err := c.Catalog(context.Background(), "account-a", true)
	require.NoError(t, err)
	assert.Equal(t, a.IDs(), refreshed.IDs())
}

func TestCatalogExactCombinationsAndIntersection(t *testing.T) {
	picker, usable := catalogFixture(t)
	cat, err := parseCatalog(picker, usable, time.Now())
	require.NoError(t, err)
	model, ok := cat.Model("grok-4.7")
	require.True(t, ok)
	restricted := model
	restricted.Variants = []ModelVariant{model.Variants[0]}
	intersection := model.Intersect(restricted)
	require.Len(t, intersection.Variants, 1)
	assert.Len(t, model.Variants, 16) // Intersection cannot mutate the account cache.
	for _, p := range intersection.Parameters {
		assert.Len(t, p.Values, 1)
	}
	assert.NotEmpty(t, intersection.Intersect(CatalogModel{Error: "unavailable"}).Error)
	// All individual values exist, but this combination does not.
	broken := model
	broken.Variants = []ModelVariant{model.Variants[0], model.Variants[len(model.Variants)-1]}
	r := &chatRequest{Model: model.ID}
	value := maps.Clone(broken.Variants[0].Parameters)
	value["fast"] = broken.Variants[1].Parameters["fast"]
	raw, _ := common.Marshal(map[string]any{"model": model.ID, "messages": []any{map[string]string{"role": "user", "content": "hello"}}, "cursor": map[string]any{"parameters": value}})
	r, _, _, _, err = parseRequest(raw)
	require.NoError(t, err)
	_, err = resolveModel(&Catalog{Models: []CatalogModel{broken}}, r)
	require.ErrorContains(t, err, "combination")
}

func TestExhaustedOtherModelsDoesNotCoolCursorModels(t *testing.T) {
	var runs atomic.Int32
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/exchange_user_api_key":
			io.WriteString(w, `{"accessToken":"fixture-token"}`)
		case "/aiserver.v1.ServerConfigService/GetServerConfig":
			io.WriteString(w, `{}`)
		case "/aiserver.v1.DashboardService/GetCurrentPeriodUsage":
			io.WriteString(w, `{"planUsage":{"autoPercentUsed":2.7,"apiPercentUsed":100}}`)
		case "/agent.v1.AgentService/Run":
			n := runs.Add(1)
			if n == 1 {
				w.Header().Set("Retry-After", "600")
				w.WriteHeader(429)
				io.WriteString(w, `{"code":"resource_exhausted"}`)
				return
			}
			w.Write(connectFrame(pb{}.bytes(1, pb{}.bytes(1, pb{}.str(1, "healthy")))))
			w.Write(connectFrame(pb{}.bytes(1, pb{}.bytes(14, pb{}.varint(1, 10).varint(2, 2)))))
		default:
			http.NotFound(w, r)
		}
	})
	for _, tc := range []struct {
		model  string
		status int
	}{{"claude-opus-5-5", 429}, {"grok-4.7", 200}, {"composer-2.5", 200}, {"claude-opus-5-5", 429}} {
		res := c.Chat(context.Background(), "same-account", []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, tc.model)))
		b, err := io.ReadAll(res.Body)
		res.Body.Close()
		require.NoError(t, err)
		require.Equal(t, tc.status, res.StatusCode, "%s: %s", tc.model, b)
	}
	require.EqualValues(t, 3, runs.Load(), "Other Models retry must be rejected locally")
}

func TestQuotaIsolationRequiresFreshUnambiguousEvidence(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, tc := range []struct {
		name     string
		windows  []QuotaWindow
		scope    BudgetScope
		expired  bool
		isolated bool
	}{
		{name: "other exhausted", windows: []QuotaWindow{{"Cursor Models", 3}, {"Other Models", 100}}, scope: OtherModels, isolated: true},
		{name: "cursor exhausted", windows: []QuotaWindow{{"Cursor Models", 100}, {"Other Models", 3}}, scope: CursorModels, isolated: true},
		{name: "both exhausted", windows: []QuotaWindow{{"Cursor Models", 100}, {"Other Models", 100}}, scope: OtherModels},
		{name: "missing peer", windows: []QuotaWindow{{"Other Models", 100}}, scope: OtherModels},
		{name: "below limit", windows: []QuotaWindow{{"Cursor Models", 3}, {"Other Models", 99}}, scope: OtherModels},
		{name: "expired", windows: []QuotaWindow{{"Cursor Models", 3}, {"Other Models", 100}}, scope: OtherModels, expired: true},
		{name: "unknown vendor", windows: []QuotaWindow{{"Cursor Models", 3}, {"Other Models", 100}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expires := now.Add(time.Second)
			if tc.expired {
				expires = now
			}
			q := &QuotaSnapshot{ExpiresAt: expires, Windows: tc.windows}
			require.Equal(t, tc.isolated, q.isolatedExhaustion(tc.scope, now))
		})
	}
	require.Empty(t, vendorBudgetScope(""), "display fallback must not classify billing")
	require.Empty(t, vendorBudgetScope("future-vendor"))
}

func TestQuotaWindowConcurrentCompletionCannotClearCooldown(t *testing.T) {
	c := NewClient()
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	a, err := c.account("key")
	require.NoError(t, err)
	a.quota = &QuotaSnapshot{ExpiresAt: now.Add(time.Hour), Windows: []QuotaWindow{{"Cursor Models", 2}, {"Other Models", 100}}}
	for range 2 {
		require.Zero(t, c.acquire(a))
		require.Zero(t, c.acquireWindow(a, OtherModels))
	}
	c.finishRun(context.Background(), "key", a, OtherModels, 429, 10*time.Minute, false)
	c.finishRun(context.Background(), "key", a, OtherModels, 200, 0, false)
	require.Zero(t, c.acquire(a))
	require.Zero(t, c.acquireWindow(a, CursorModels))
	c.finishRun(context.Background(), "key", a, CursorModels, 200, 0, false)
	require.Equal(t, 429, c.acquireWindow(a, OtherModels))
	require.Zero(t, c.acquire(a))
	require.Zero(t, c.acquireWindow(a, CursorModels))
	require.Zero(t, c.acquire(a))
	require.Zero(t, c.acquireWindow(a, CursorModels))
	require.Equal(t, 429, c.acquire(a), "account concurrency remains bounded")
	c.finishRun(context.Background(), "key", a, CursorModels, 200, 0, false)
	c.finishRun(context.Background(), "key", a, CursorModels, 200, 0, false)
	now = now.Add(10 * time.Minute)
	require.Zero(t, c.acquire(a))
	require.Zero(t, c.acquireWindow(a, OtherModels))
	require.Equal(t, 429, c.acquireWindow(a, OtherModels), "one recovery probe per window")
	c.finishRun(context.Background(), "key", a, OtherModels, 504, 0, true)
	require.Zero(t, c.acquire(a))
	require.Zero(t, c.acquireWindow(a, OtherModels))
	c.finishRun(context.Background(), "key", a, OtherModels, 429, 0, false)
	require.Equal(t, now.Add(30*time.Second), a.windows[OtherModels].openUntil)
	now = now.Add(30 * time.Second)
	require.Zero(t, c.acquire(a))
	require.Zero(t, c.acquireWindow(a, OtherModels))
	c.finishRun(context.Background(), "key", a, OtherModels, 200, 0, false)
	status, err := c.Health("key")
	require.NoError(t, err)
	require.Empty(t, status.Cooldowns)
}

func TestUnknown429StillCoolsEntireAccount(t *testing.T) {
	c := NewClient()
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	a, err := c.account("key")
	require.NoError(t, err)
	a.quota = &QuotaSnapshot{ExpiresAt: now.Add(time.Minute), Windows: []QuotaWindow{{"Cursor Models", 2}, {"Other Models", 30}}}
	require.Zero(t, c.acquire(a))
	require.Zero(t, c.acquireWindow(a, OtherModels))
	c.finishRun(context.Background(), "key", a, OtherModels, 429, 600*time.Second, false)
	require.Equal(t, 429, c.acquire(a))
	status, err := c.Health("key")
	require.NoError(t, err)
	require.Len(t, status.Cooldowns, 1)
	require.Equal(t, "account", status.Cooldowns[0].Scope)
}

func TestQuotaCoalescesAndFailedRefreshDoesNotServeStaleUsage(t *testing.T) {
	var queries atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	var fail atomic.Bool
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/exchange_user_api_key":
			io.WriteString(w, `{"accessToken":"fixture-token"}`)
		case "/aiserver.v1.ServerConfigService/GetServerConfig":
			io.WriteString(w, `{}`)
		case "/aiserver.v1.DashboardService/GetCurrentPeriodUsage":
			if queries.Add(1) == 1 {
				close(entered)
				<-release
			}
			if fail.Load() {
				w.WriteHeader(503)
				return
			}
			io.WriteString(w, `{"planUsage":{"autoPercentUsed":0,"apiPercentUsed":100}}`)
		default:
			http.NotFound(w, r)
		}
	})
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			q, err := c.Quota(context.Background(), "key")
			assert.NoError(t, err)
			if assert.NotNil(t, q) {
				assert.Len(t, q.Windows, 2)
			}
		})
	}
	<-entered
	close(release)
	wg.Wait()
	require.EqualValues(t, 1, queries.Load())
	now = now.Add(31 * time.Second)
	fail.Store(true)
	for range 2 {
		q, err := c.Quota(context.Background(), "key")
		require.Error(t, err)
		require.Nil(t, q)
	}
	require.EqualValues(t, 2, queries.Load(), "failed refresh should back off")
}

func TestCursorOnDemandPreservesUnknownAndAllLimits(t *testing.T) {
	for _, tc := range []struct{ name, spend, hard, enabled, state string }{
		{"missing", `{}`, `{}`, "unknown", "unknown"},
		{"malformed enabled flag", `{}`, `{"noUsageBasedAllowed":"no"}`, "unknown", "unknown"},
		{"inconsistent team caps", `{"limitType":"team","individualLimit":1000,"individualUsed":20,"pooledLimit":"10000","pooledUsed":500}`, `{"hardLimit":200}`, "enabled", "unknown"},
		{"explicit false is enabled but budget unknown", `{}`, `{"noUsageBasedAllowed":false}`, "enabled", "unknown"},
		{"explicit disabled", `{}`, `{"noUsageBasedAllowed":true}`, "disabled", "disabled"},
		{"zero cap is disabled", `{}`, `{"hardLimit":0}`, "disabled", "disabled"},
		{"team member exhausted despite team balance", `{"limitType":"team","individualLimit":131000,"individualUsed":131022,"pooledLimit":"800000000","pooledUsed":209521520}`, `{"hardLimit":8000000}`, "enabled", "exhausted"},
		{"team exhausted despite member balance", `{"limitType":"team","individualLimit":1000,"individualUsed":20,"pooledLimit":"10000","pooledUsed":10000}`, `{"hardLimit":100}`, "enabled", "exhausted"},
		{"both team caps known", `{"limitType":"team","individualLimit":1000,"individualUsed":20,"pooledLimit":"10000","pooledUsed":500}`, `{"hardLimit":100}`, "enabled", "remaining"},
		{"missing member cap is not unlimited", `{"limitType":"team","pooledLimit":"10000","pooledUsed":500}`, `{"hardLimit":100}`, "enabled", "unknown"},
		{"missing team cap is not unlimited", `{"limitType":"team","individualLimit":1000,"individualUsed":20}`, `{"hardLimit":100}`, "enabled", "unknown"},
		{"personal cents and dollars", `{"limitType":"user","individualUsed":1250}`, `{"hardLimit":20}`, "enabled", "remaining"},
		{"missing spend is not zero", `{"limitType":"user"}`, `{"hardLimit":20}`, "enabled", "unknown"},
		{"settings failure preserves member exhaustion", `{"limitType":"team","individualLimit":1000,"individualUsed":1000}`, ``, "unknown", "exhausted"},
		{"unknown scope", `{"limitType":"future","individualUsed":0}`, `{"hardLimit":20}`, "enabled", "unknown"},
		{"malformed settings", `{}`, `{"hardLimit":-1}`, "unknown", "unknown"},
		{"negative member usage", `{"limitType":"user","individualUsed":-1}`, `{"hardLimit":20}`, "enabled", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enabled, budget := parseOnDemand([]byte(tc.spend), []byte(tc.hard))
			assert.Equal(t, tc.enabled, enabled)
			assert.Equal(t, tc.state, budget.State)
			if tc.name == "team member exhausted despite team balance" {
				require.Len(t, budget.Limits, 2)
				assert.Equal(t, 1310.22, *budget.Limits[0].UsedUSD)
				assert.Equal(t, 1310.0, *budget.Limits[0].LimitUSD)
				assert.Equal(t, 0.0, *budget.Limits[0].RemainingUSD)
				assert.Equal(t, 5904784.8, *budget.Limits[1].RemainingUSD)
			}
		})
	}
}
func TestCursorBillingNumbersAndResetValidation(t *testing.T) {
	for _, raw := range []string{`null`, `-1`, `"NaN"`, `"Infinity"`, `1.1`, `true`, `{}`, `9007199254740992`, `"1e100"`, `"1e-2147483647"`, `"1e2147483647"`} {
		assert.Nil(t, dashboardCents([]byte(raw), false), raw)
	}
	assert.EqualValues(t, 1250, *dashboardCents([]byte(`"1250"`), false))
	assert.EqualValues(t, 1234, *dashboardCents([]byte(`12.34`), true))
	assert.Nil(t, dashboardCents([]byte(`12.345`), true))
	now := time.UnixMilli(1790000000000)
	for _, raw := range []string{"", "unknown", "-1", "0", "253402300800000", "179337639500000000000", "NaN"} {
		at, state := quotaReset(raw, now)
		assert.Nil(t, at)
		assert.Equal(t, "unknown", state)
	}
	at, state := quotaReset("1793376395000", now)
	require.NotNil(t, at)
	assert.Equal(t, "known", state)
	assert.EqualValues(t, 1793376395000, at.UnixMilli())
	_, state = quotaReset("1793376395000", *at)
	assert.Equal(t, "elapsed", state)
	q := &QuotaSnapshot{ExpiresAt: now.Add(time.Minute), Windows: []QuotaWindow{{"Cursor Models", 2}, {"Other Models", 100}}, OnDemandBudget: OnDemandBudget{State: "remaining"}}
	assert.False(t, q.isolatedExhaustion(OtherModels, now), "known paid headroom makes 429 attribution ambiguous")
}
func TestCursorQuotaOptionalSettingsFailureRetainsUsageAndReset(t *testing.T) {
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/exchange_user_api_key":
			io.WriteString(w, `{"accessToken":"fixture"}`)
		case "/aiserver.v1.ServerConfigService/GetServerConfig":
			io.WriteString(w, `{}`)
		case "/aiserver.v1.DashboardService/GetCurrentPeriodUsage":
			io.WriteString(w, `{"planUsage":{"autoPercentUsed":3,"apiPercentUsed":100},"billingCycleEnd":"1800000000000"}`)
		case "/aiserver.v1.DashboardService/GetHardLimit":
			w.WriteHeader(503)
			io.WriteString(w, `{"secret":"never-include-me"}`)
		default:
			http.NotFound(w, r)
		}
	})
	q, err := c.Quota(context.Background(), "key")
	require.NoError(t, err)
	assert.Len(t, q.Windows, 2)
	assert.Equal(t, "unknown", q.OnDemand)
	assert.Equal(t, "unknown", q.OnDemandBudget.State)
	assert.Equal(t, "known", q.ResetState)
	require.NotNil(t, q.ResetsAt)
	b, err := common.Marshal(q)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "never-include-me")
}
func TestCursorHealthObservationsArePerPoolAndCancellationIsNeutral(t *testing.T) {
	c := NewClient()
	a, err := c.account("key")
	require.NoError(t, err)
	require.Zero(t, c.acquire(a))
	require.Zero(t, c.acquireWindow(a, CursorModels))
	c.finishRun(context.Background(), "key", a, CursorModels, 200, 0, false)
	require.Zero(t, c.acquire(a))
	require.Zero(t, c.acquireWindow(a, OtherModels))
	c.finishRun(context.Background(), "key", a, OtherModels, 400, 0, false)
	require.Zero(t, c.acquire(a))
	require.Zero(t, c.acquireWindow(a, CursorModels))
	c.finishRun(context.Background(), "key", a, CursorModels, 504, 0, true)
	h, err := c.Health("key")
	require.NoError(t, err)
	require.Len(t, h.LastRequests, 2)
	assert.Equal(t, string(CursorModels), h.LastRequests[0].Scope)
	assert.Equal(t, 200, h.LastRequests[0].Status)
	assert.Equal(t, string(OtherModels), h.LastRequests[1].Scope)
	assert.Equal(t, 400, h.LastRequests[1].Status)
	assert.False(t, h.Disabled)
}

func TestCursorStreamCloseAfterFinishStillRecordsCompletedRun(t *testing.T) {
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/exchange_user_api_key":
			io.WriteString(w, `{"accessToken":"fixture"}`)
		case "/aiserver.v1.ServerConfigService/GetServerConfig":
			io.WriteString(w, `{}`)
		case "/agent.v1.AgentService/Run":
			_, err := readConnectFrame(bufio.NewReader(r.Body))
			if !assert.NoError(t, err) {
				return
			}
			w.Write(connectFrame(pb{}.bytes(1, pb{}.bytes(1, pb{}.str(1, "42")))))
			w.Write(connectFrame(pb{}.bytes(1, pb{}.bytes(14, pb{}.varint(1, 10).varint(2, 2)))))
		default:
			http.NotFound(w, r)
		}
	})
	res := c.Chat(context.Background(), "key", []byte(`{"model":"grok-4.7","stream":true,"messages":[{"role":"user","content":"compute"}]}`))
	require.Equal(t, 200, res.StatusCode)
	reader := bufio.NewReader(res.Body)
	for {
		line, err := reader.ReadString('\n')
		require.NoError(t, err)
		if strings.Contains(line, `"finish_reason":"stop"`) {
			break
		}
	}
	// Some consumers close as soon as finish_reason arrives, before usage/DONE.
	require.NoError(t, res.Body.Close())
	require.Eventually(t, func() bool { h, _ := c.Health("key"); return h.InFlight == 0 }, time.Second, time.Millisecond)
	h, err := c.Health("key")
	require.NoError(t, err)
	require.Len(t, h.LastRequests, 1)
	assert.Equal(t, 200, h.LastRequests[0].Status)
	assert.Equal(t, string(CursorModels), h.LastRequests[0].Scope)
}

func TestRoutingObservationsNeverTurnUnknownQuotaIntoDenial(t *testing.T) {
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	c := NewClientWithClock(func() time.Time { return now })
	assert.Equal(t, RoutingStatus{}, c.Routing("cold", "claude"))
	a, err := c.account("key")
	require.NoError(t, err)
	a.catalog = &Catalog{Models: []CatalogModel{{ID: "claude", BudgetScope: OtherModels}, {ID: "grok", BudgetScope: CursorModels}}}
	for _, tc := range []struct {
		name, paid       string
		expired, limited bool
	}{
		{"paid disabled", "disabled", false, true}, {"paid exhausted", "exhausted", false, true},
		{"paid available", "remaining", false, false}, {"paid unknown", "unknown", false, false}, {"stale quota", "disabled", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a.quota = &QuotaSnapshot{ExpiresAt: now.Add(time.Second), Windows: []QuotaWindow{{Name: string(OtherModels), UsedPercent: 100}}, OnDemandBudget: OnDemandBudget{State: tc.paid}}
			if tc.expired {
				a.quota.ExpiresAt = now
			}
			got := c.Routing("key", "claude")
			assert.Empty(t, got.Blocked)
			assert.Equal(t, tc.limited, got.QuotaLimited)
			assert.False(t, c.Routing("key", "grok").QuotaLimited)
			assert.False(t, c.Routing("key", "unknown-model").QuotaLimited)
		})
	}
	a.windows = map[BudgetScope]*health{OtherModels: {openUntil: now.Add(time.Second)}}
	assert.Equal(t, "quota_pool_cooling", c.Routing(" key ", "claude").Blocked)
	assert.Empty(t, c.Routing("key", "grok").Blocked)
	assert.Empty(t, c.Routing("key", "unknown-model").Blocked)
	now = now.Add(time.Second)
	a.quota.ExpiresAt = now.Add(time.Minute)
	got := c.Routing("key", "claude")
	assert.True(t, got.NeedsProbe)
	assert.False(t, got.QuotaLimited)
	require.Zero(t, c.acquire(a))
	require.Zero(t, c.acquireWindow(a, OtherModels))
	assert.Equal(t, "quota_pool_busy", c.Routing("key", "claude").Blocked)
	assert.Empty(t, c.Routing("key", "grok").Blocked)
	require.Zero(t, c.acquire(a))
	assert.Equal(t, "account_busy", c.Routing("key", "grok").Blocked)
	c.finish(a, 200, 0, true)
	c.finishRun(context.Background(), "key", a, OtherModels, 200, 0, false)
	assert.Empty(t, c.Routing("key", "claude").Blocked)
	assert.False(t, c.Routing("key", "claude").NeedsProbe)
	a.disabled = true
	assert.Equal(t, "account_disabled", c.Routing("key", "grok").Blocked)
}
