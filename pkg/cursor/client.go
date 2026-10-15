package cursor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const apiURL = "https://api2.cursor.sh"
const defaultAgent = "https://agentn.global.api5.cursor.sh"
const clientVersion = "cli-2026.09.23-86fc751"

// Client owns transport and ephemeral per-key health/auth state. No account
// credentials, token payloads or upstream error bodies are logged.
type Client struct {
	http                       *http.Client
	api                        string
	mu                         sync.Mutex
	accounts                   map[[32]byte]*account
	now                        func() time.Time
	maxInFlight                int
	firstTimeout, totalTimeout time.Duration
}
type account struct {
	mu           sync.Mutex
	token, agent string
	expires      time.Time
	health
	windows        map[BudgetScope]*health
	quota          *QuotaSnapshot
	quotaLoading   chan struct{}
	quotaError     error
	quotaRetryAt   time.Time
	disabled       bool
	catalog        *Catalog
	catalogLoading chan struct{}
	catalogError   error
	catalogRetryAt time.Time
}

func NewClient() *Client {
	return NewClientWithClock(time.Now)
}

// NewClientWithClock makes cache expiry and recovery deterministic for local
// simulations. now must be concurrency-safe; upstream hosts remain fixed.
func NewClientWithClock(now func() time.Time) *Client {
	if now == nil {
		now = time.Now
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ForceAttemptHTTP2 = true
	t.ResponseHeaderTimeout = 20 * time.Second
	// Read once at construction so admission, routing and health share an
	// immutable per-account limit. This is local protection, not a vendor limit.
	limit, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CURSOR_MAX_IN_FLIGHT")))
	if err != nil || limit < 1 || limit > 64 {
		limit = 2
	}
	return &Client{http: &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, api: apiURL, accounts: make(map[[32]byte]*account), now: now, maxInFlight: limit, firstTimeout: 60 * time.Second, totalTimeout: 90 * time.Second}
}

var Default = NewClient()

func (c *Client) account(key string) (*account, error) {
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("Cursor User API Key is required")
	}
	h := sha256.Sum256([]byte(strings.TrimSpace(key)))
	c.mu.Lock()
	defer c.mu.Unlock()
	if a := c.accounts[h]; a != nil {
		return a, nil
	}
	// Bound retained tokens and keys' hashes. Restart/rotation clears state.
	if len(c.accounts) >= 1024 {
		return nil, errors.New("Cursor account cache capacity reached")
	}
	a := &account{}
	c.accounts[h] = a
	return a, nil
}

func (c *Client) acquire(a *account) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.disabled {
		return 401
	}
	return a.health.acquire(c.now(), c.maxInFlight)
}
func (c *Client) finish(a *account, status int, retry time.Duration, neutral bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if status == 401 && !neutral {
		a.disabled = true
		a.token = ""
	}
	a.health.finish(c.now(), status, retry, neutral)
}

func headers(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("x-cursor-client-version", clientVersion)
	req.Header.Set("x-cursor-client-type", "cli")
	req.Header.Set("x-ghost-mode", "true")
	req.Header.Set("x-request-id", cursorUUID())
}

func (c *Client) post(ctx context.Context, path, token string) ([]byte, int, error) {
	return c.postJSON(ctx, path, token, "{}", 1<<20)
}

func (c *Client) postJSON(ctx context.Context, path, token, body string, limit int64) ([]byte, int, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.api+path, strings.NewReader(body))
	headers(req, token)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, 502, errors.New("Cursor service unavailable")
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, 502, errors.New("invalid Cursor service response")
	}
	if res.StatusCode/100 != 2 {
		return nil, res.StatusCode, errors.New("Cursor service rejected request")
	}
	return b, res.StatusCode, nil
}

func validAgent(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && strings.HasSuffix(u.Hostname(), ".cursor.sh") && u.Port() == "" && u.User == nil && (u.Path == "" || u.Path == "/") && u.RawQuery == "" && u.Fragment == ""
}

func (c *Client) authenticate(ctx context.Context, a *account, key string) (string, string, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && c.now().Before(a.expires) {
		return a.token, a.agent, 200
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	b, status, err := c.post(ctx, "/auth/exchange_user_api_key", key)
	if err != nil {
		return "", "", status
	}
	var auth struct {
		AccessToken string `json:"accessToken"`
	}
	if common.Unmarshal(b, &auth) != nil || auth.AccessToken == "" {
		return "", "", 502
	}
	expires := c.now().Add(10 * time.Minute)
	if parts := strings.Split(auth.AccessToken, "."); len(parts) == 3 {
		if payload, e := base64.RawURLEncoding.DecodeString(parts[1]); e == nil {
			var claims struct {
				Exp int64 `json:"exp"`
			}
			if common.Unmarshal(payload, &claims) == nil && claims.Exp > 0 {
				// Only a cache expiry hint; trust comes from the TLS exchange, not JWT parsing.
				jwtExpiry := time.Unix(claims.Exp, 0).Add(-time.Minute)
				if jwtExpiry.Before(expires) {
					expires = jwtExpiry
				}
			}
		}
	}
	if !expires.After(c.now()) {
		return "", "", 401
	}
	agent := defaultAgent
	if config, _, e := c.post(ctx, "/aiserver.v1.ServerConfigService/GetServerConfig", auth.AccessToken); e == nil {
		var cfg struct {
			AgentURLConfig struct {
				AgentURL  string `json:"agentUrl"`
				AgentNURL string `json:"agentnUrl"`
			} `json:"agentUrlConfig"`
		}
		if common.Unmarshal(config, &cfg) == nil {
			candidate := cfg.AgentURLConfig.AgentURL
			if candidate == "" {
				candidate = cfg.AgentURLConfig.AgentNURL
			}
			if candidate != "" {
				if !validAgent(candidate) {
					return "", "", 502
				}
				agent = strings.TrimRight(candidate, "/")
			}
		}
	}
	a.token, a.agent, a.expires = auth.AccessToken, agent, expires
	return a.token, a.agent, 200
}

func errorResponse(status int, message string) *http.Response {
	return errorResponseWithCode(status, message, "cursor_upstream_error")
}

func errorResponseWithCode(status int, message, code string) *http.Response {
	if status < 400 || status > 599 {
		status = 502
	}
	if code == "" {
		code = "cursor_upstream_error"
	}
	b, _ := common.Marshal(map[string]any{"error": map[string]any{"message": message, "type": "cursor_error", "code": code}})
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(b))}
}
func statusMessage(status int) string {
	switch status {
	case 401:
		return "Cursor authentication failed"
	case 403:
		return "Cursor access denied"
	case 429:
		return "Cursor account is busy or rate limited"
	case 504:
		return "Cursor request timed out"
	default:
		return "Cursor upstream request failed"
	}
}

// cursorFailure classifies provider errors but never echoes their body or token.
func cursorFailure(status int, body []byte) (int, string) {
	type failure struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details []struct {
			Debug struct {
				Details struct {
					Title string `json:"title"`
				} `json:"details"`
			} `json:"debug"`
		} `json:"details"`
	}
	var f struct {
		failure
		Error *failure `json:"error"`
	}
	if common.Unmarshal(body, &f) != nil {
		return 502, statusMessage(502)
	}
	v := f.failure
	if f.Error != nil {
		v = *f.Error
	}
	for _, detail := range v.Details {
		if detail.Debug.Details.Title == "Update Required" {
			return 400, "Cursor protocol client version requires an update"
		}
	}
	if v.Code == "" && f.Error == nil && status/100 == 2 {
		return status, ""
	}
	switch v.Code {
	case "unauthenticated":
		status = 401
	case "permission_denied":
		status = 403
	case "resource_exhausted":
		status = 429
	case "invalid_argument":
		status = 400
	case "unavailable":
		status = 503
	default:
		if status/100 == 2 {
			status = 502
		}
	}
	return status, statusMessage(status)
}

type responseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *responseBody) Close() error { b.cancel(); return b.ReadCloser.Close() }

// Chat translates a complete OpenAI chat request into a bounded Cursor Run.
// Closing the response cancels both directions; failed streams are never replayed.
func (c *Client) Chat(parent context.Context, key string, body []byte) *http.Response {
	r, msgs, tools, last, err := parseRequest(body)
	if err != nil {
		return errorResponse(400, err.Error())
	}
	a, err := c.account(key)
	if err != nil {
		return errorResponse(400, err.Error())
	}
	if status := c.acquire(a); status != 0 {
		return errorResponse(status, statusMessage(status))
	}
	ctx, cancel := context.WithTimeout(parent, c.totalTimeout)
	deadline, _ := ctx.Deadline()
	token, agent, status := c.authenticate(ctx, a, key)
	if status != 200 {
		cancel()
		c.finish(a, status, 0, parent.Err() != nil)
		return errorResponse(status, statusMessage(status))
	}
	catalog, err := c.Catalog(ctx, key, false)
	if err != nil {
		cancel()
		c.finish(a, 503, 0, true)
		return errorResponse(503, err.Error())
	}
	r.selection, err = resolveModel(catalog, r)
	if err != nil {
		cancel()
		c.finish(a, 400, 0, true)
		return errorResponse(400, err.Error())
	}
	scope := r.selection.BudgetScope
	if status := c.acquireWindow(a, scope); status != 0 {
		cancel()
		c.finish(a, status, 0, true)
		return errorResponse(status, string(scope)+" quota cooldown is active; retry after it expires")
	}
	finish := func(status int, retry time.Duration, neutral bool) string {
		finishCtx, finishCancel := context.WithDeadline(parent, deadline)
		defer finishCancel()
		return c.finishRun(finishCtx, key, a, scope, status, retry, neutral)
	}
	run, blobs := buildCursorRun(msgs, last, tools, r.selection, cursorUUID())
	pr, pw := io.Pipe()
	st := &cursorStream{pw: pw, blobs: blobs}
	stopPipe := context.AfterFunc(ctx, func() { pr.CloseWithError(ctx.Err()); pw.CloseWithError(ctx.Err()) })
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, agent+"/agent.v1.AgentService/Run", pr)
	headers(req, token)
	req.Header.Set("Content-Type", "application/connect+proto")
	req.Header.Set("x-cursor-agent-allowed-tools", "mcp_tool_call,get_mcp_tools_tool_call")
	go func() { st.send(run); st.heartbeat(ctx) }()
	res, err := c.http.Do(req)
	if err != nil {
		cancel()
		stopPipe()
		finish(502, 0, parent.Err() != nil)
		return errorResponse(502, statusMessage(502))
	}
	cleanup := func() { cancel(); stopPipe(); res.Body.Close(); pr.Close(); pw.Close() }
	if res.StatusCode/100 != 2 {
		status = res.StatusCode
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if len(b) > 0 {
			status, _ = cursorFailure(status, b)
		}
		retry := retryDelay(res.Header.Get("Retry-After"), c.now())
		cleanup()
		message := finish(status, retry, parent.Err() != nil)
		return errorResponse(status, message)
	}
	events := make(chan Event, 16)
	go st.decode(ctx, bufio.NewReader(res.Body), events, tools)
	// Heartbeats do not satisfy this deadline. No successful HTTP response until
	// the first actual model/tool event, so early failures can use host retry.
	firstTimer := time.NewTimer(c.firstTimeout)
	var first Event
	select {
	case event, ok := <-events:
		first = event
		if !ok {
			first = Event{Kind: KError, Status: 502, Text: "Cursor stream ended before completion"}
		}
	case <-firstTimer.C:
		first = Event{Kind: KError, Status: 504, Text: statusMessage(504)}
	case <-ctx.Done():
		first = Event{Kind: KError, Status: 504, Text: statusMessage(504)}
	}
	firstTimer.Stop()
	if first.Kind == KError {
		cleanup()
		message := finish(first.Status, 0, parent.Err() != nil)
		if first.Status != 429 {
			message = first.Text
		}
		return errorResponseWithCode(first.Status, message, first.ErrorCode)
	}
	outR, outW := io.Pipe()
	stopOut := context.AfterFunc(ctx, func() { outW.CloseWithError(ctx.Err()) })
	go func() {
		status, completed := writeChat(ctx, outW, r, first, events)
		neutral := parent.Err() != nil || errors.Is(ctx.Err(), context.Canceled)
		// A consumer may close after finish_reason without draining usage/DONE.
		// The upstream stop event still proves Run completion. Incomplete canceled
		// streams remain neutral; only observed completion can recover a circuit.
		if completed {
			status, neutral = 200, false
		}
		stopOut()
		cleanup()
		finish(status, 0, neutral)
		outW.Close()
	}()
	contentType := "application/json"
	if r.Stream != nil && *r.Stream {
		contentType = "text/event-stream"
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: &responseBody{ReadCloser: outR, cancel: cancel}}
}

func retryDelay(value string, now time.Time) time.Duration {
	if n, e := strconv.ParseInt(value, 10, 64); e == nil && n >= 0 {
		if n > int64((365*24*time.Hour)/time.Second) {
			return 365 * 24 * time.Hour
		}
		return time.Duration(n) * time.Second
	}
	if t, e := http.ParseTime(value); e == nil {
		return max(0, t.Sub(now))
	}
	return 0
}

func writeChat(ctx context.Context, w io.Writer, r *chatRequest, first Event, events <-chan Event) (int, bool) {
	stream := r.Stream != nil && *r.Stream
	id := "chatcmpl-" + cursorUUID()
	created := time.Now().Unix()
	var text, thinking strings.Builder
	var calls []toolCall
	var usage Usage
	var responseBytes int
	write := func(v any) bool {
		b, e := common.Marshal(v)
		if e != nil {
			return false
		}
		if stream {
			b = append(append([]byte("data: "), b...), []byte("\n\n")...)
		}
		_, e = w.Write(b)
		return e == nil
	}
	chunk := func(delta any, finish any) map[string]any {
		return map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": r.Model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	}
	ev := first
	for {
		responseBytes += len(ev.Text)
		if responseBytes > 8<<20 || len(calls) > 128 {
			ev = Event{Kind: KError, Status: 502, Text: "Cursor response exceeded the native demo limit"}
		}
		delta := map[string]any{}
		switch ev.Kind {
		case KText:
			text.WriteString(ev.Text)
			delta["content"] = ev.Text
		case KThink:
			thinking.WriteString(ev.Text)
			delta["reasoning_content"] = ev.Text
		case KToolStart:
			t := toolCall{ID: ev.ID, Type: "function"}
			t.Function.Name = ev.Name
			calls = append(calls, t)
			delta["tool_calls"] = []any{map[string]any{"index": len(calls) - 1, "id": ev.ID, "type": "function", "function": map[string]any{"name": ev.Name, "arguments": ""}}}
		case KToolArgs:
			if len(calls) == 0 {
				return 502, false
			}
			calls[len(calls)-1].Function.Arguments += ev.Text
			delta["tool_calls"] = []any{map[string]any{"index": len(calls) - 1, "function": map[string]any{"arguments": ev.Text}}}
		case KUsage:
			usage = ev.Usage
		case KError:
			code := ev.ErrorCode
			if code == "" {
				code = "incomplete_stream"
			}
			write(map[string]any{"error": map[string]any{"message": ev.Text, "type": "cursor_error", "code": code}})
			return ev.Status, false
		case KStop:
			reason := "stop"
			if ev.Stop == "tool" {
				reason = "tool_calls"
			}
			prompt := usage.Input + usage.CacheRead + usage.CacheWrite
			counts := map[string]any{"prompt_tokens": prompt, "completion_tokens": usage.Output, "total_tokens": prompt + usage.Output, "prompt_tokens_details": map[string]any{"cached_tokens": usage.CacheRead}, "completion_tokens_details": map[string]any{"reasoning_tokens": usage.Reasoning}}
			if stream {
				if !write(chunk(map[string]any{}, reason)) {
					return 502, true
				}
				if prompt > 0 && !write(map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": r.Model, "choices": []any{}, "usage": counts}) {
					return 502, true
				}
				if _, e := io.WriteString(w, "data: [DONE]\n\n"); e != nil {
					return 502, true
				}
			} else {
				message := map[string]any{"role": "assistant", "content": text.String()}
				if thinking.Len() > 0 {
					message["reasoning_content"] = thinking.String()
				}
				if len(calls) > 0 {
					message["tool_calls"] = calls
				}
				result := map[string]any{"id": id, "object": "chat.completion", "created": created, "model": r.Model, "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": reason}}}
				if prompt > 0 {
					result["usage"] = counts
				}
				if !write(result) {
					return 502, true
				}
			}
			return 200, true
		}
		if stream && len(delta) > 0 && !write(chunk(delta, nil)) {
			return 502, false
		}
		select {
		case next, ok := <-events:
			if ok {
				ev = next
			} else {
				ev = Event{Kind: KError, Status: 502, Text: "Cursor stream ended before completion"}
			}
		case <-ctx.Done():
			return 504, false
		}
	}
}
