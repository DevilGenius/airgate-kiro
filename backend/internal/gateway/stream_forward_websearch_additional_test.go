package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/DevilGenius/airgate-sdk/sdkgo"
)

func eventFrame(eventType string, payload string) []byte {
	return buildEventStreamFrame(
		map[string]string{":message-type": "event", ":event-type": eventType},
		[]byte(payload),
	)
}

func errorFrame(messageType, code, payload string) []byte {
	headers := map[string]string{":message-type": messageType}
	if messageType == "error" {
		headers[":error-code"] = code
	} else {
		headers[":exception-type"] = code
	}
	return buildEventStreamFrame(headers, []byte(payload))
}

func httpResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestStreamKiroToSSEConvertsEvents(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(eventFrame("contextUsageEvent", `{"contextUsagePercentage":25}`))
	stream.Write(eventFrame("assistantResponseEvent", `{"content":"before <thinking>secret"}`))
	stream.Write(eventFrame("assistantResponseEvent", `{"content":" done</thinking>\n\nafter"}`))
	stream.Write(eventFrame("toolUseEvent", `{"name":"short","toolUseId":"toolu_1","input":"{\"x\":1}","stop":true}`))

	rr := httptest.NewRecorder()
	convCtx := &ConvertContext{
		AnthropicModel: "claude-sonnet-4-6",
		ContextWindow:  400,
		ToolNameMap:    map[string]string{"short": "mcp__server__long_tool_name"},
		SystemPrompt:   "stream sys",
		ToolsJSON:      `[{"name":"tool"}]`,
	}

	outcome := streamKiroToSSE(context.Background(), &stream, rr, convCtx, time.Now().Add(-time.Millisecond))
	if outcome.Kind != sdk.OutcomeSuccess {
		t.Fatalf("outcome = %+v", outcome)
	}
	if rr.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type = %q", rr.Header().Get("Content-Type"))
	}
	body := rr.Body.String()
	for _, want := range []string{"event: message_start", "thinking_delta", "text_delta", "mcp__server__long_tool_name", "stop_reason\":\"tool_use"} {
		if !strings.Contains(body, want) {
			t.Fatalf("SSE body missing %q:\n%s", want, body)
		}
	}
	if outcome.Usage.InputTokens != 100 || outcome.Usage.OutputTokens <= 0 {
		t.Fatalf("usage not populated: %+v", outcome.Usage)
	}
}

func TestStreamKiroToSSEAbortAndErrorBranches(t *testing.T) {
	convCtx := &ConvertContext{AnthropicModel: "claude-sonnet-4-6", ContextWindow: 100}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	outcome := streamKiroToSSE(cancelled, strings.NewReader(""), httptest.NewRecorder(), convCtx, time.Now())
	if outcome.Kind != sdk.OutcomeStreamAborted || !strings.Contains(outcome.Reason, "context cancelled") {
		t.Fatalf("cancelled stream outcome = %+v", outcome)
	}

	outcome = streamKiroToSSE(context.Background(), bytes.NewReader(errorFrame("error", "Throttling", "slow")), httptest.NewRecorder(), convCtx, time.Now())
	if outcome.Kind != sdk.OutcomeUpstreamTransient {
		t.Fatalf("pre-start error outcome = %+v", outcome)
	}

	var started bytes.Buffer
	started.Write(eventFrame("assistantResponseEvent", `{"content":"hello"}`))
	started.Write(errorFrame("exception", "InternalFailure", "boom"))
	outcome = streamKiroToSSE(context.Background(), &started, httptest.NewRecorder(), convCtx, time.Now())
	if outcome.Kind != sdk.OutcomeStreamAborted || !strings.Contains(outcome.Reason, "upstream error") {
		t.Fatalf("post-start error outcome = %+v", outcome)
	}
}

func TestBufferKiroResponseBuildsMessageAndHandlesErrors(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(eventFrame("contextUsageEvent", `{"contextUsagePercentage":50}`))
	stream.Write(eventFrame("assistantResponseEvent", `{"content":"Hello"}`))
	stream.Write(eventFrame("toolUseEvent", `{"name":"short","toolUseId":"toolu_1","input":"{\"path\":\"/tmp\"}","stop":true}`))

	rr := httptest.NewRecorder()
	convCtx := &ConvertContext{
		AnthropicModel: "claude-sonnet-4-6",
		ContextWindow:  200,
		ToolNameMap:    map[string]string{"short": "read_file"},
		SystemPrompt:   "buffer sys",
		ToolsJSON:      `[{"name":"tool"}]`,
	}

	outcome := bufferKiroResponse(context.Background(), &stream, rr, convCtx, time.Now().Add(-time.Millisecond))
	if outcome.Kind != sdk.OutcomeSuccess {
		t.Fatalf("outcome = %+v", outcome)
	}
	if rr.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("content type = %q", rr.Header().Get("Content-Type"))
	}
	obj := decodeObject(t, outcome.Upstream.Body)
	if obj["stop_reason"] != "tool_use" {
		t.Fatalf("response stop_reason = %#v", obj["stop_reason"])
	}
	if outcome.Usage.InputTokens != 100 || outcome.Usage.OutputTokens <= 0 {
		t.Fatalf("usage mismatch: %+v", outcome.Usage)
	}

	outcome = bufferKiroResponse(context.Background(), bytes.NewReader(errorFrame("error", "Bad", "boom")), nil, convCtx, time.Now())
	if outcome.Kind != sdk.OutcomeUpstreamTransient {
		t.Fatalf("error stream outcome = %+v", outcome)
	}
}

func TestSSEConverterSmallHelpers(t *testing.T) {
	rr := httptest.NewRecorder()
	conv := newSSEConverter(rr, &ConvertContext{AnthropicModel: "model", ContextWindow: 100}, time.Now().Add(-time.Millisecond))
	conv.handleAssistantResponse([]byte(`{bad json`))
	conv.handleContextUsage([]byte(`{bad json`))
	conv.handleToolUse([]byte(`{bad json`))
	conv.ensureTextBlock()
	conv.ensureTextBlock()
	conv.emitTextDelta("hello")
	conv.openThinkingBlock()
	conv.emitThinkingDelta("thought")
	conv.closeCurrentBlock()
	if !strings.Contains(rr.Body.String(), "text_delta") || !strings.Contains(rr.Body.String(), "thinking_delta") {
		t.Fatalf("converter output missing deltas:\n%s", rr.Body.String())
	}

	if got := jsonString("a\nb"); got != `"a\nb"` {
		t.Fatalf("jsonString = %q", got)
	}
	if got := runeAlignBack("a世", len("a世")-1); got != 1 {
		t.Fatalf("runeAlignBack = %d", got)
	}

	rr = httptest.NewRecorder()
	conv = newSSEConverter(rr, &ConvertContext{AnthropicModel: "model", ContextWindow: 100, ToolNameMap: map[string]string{}}, time.Now())
	conv.processTextWithThinking("prefix only without a full thinking tag yet")
	conv.processTextWithThinking("<thinking>partial thinking that is deliberately long enough to buffer safely")
	conv.processTextWithThinking(" and now done</thinking>tail")
	conv.closeThinkingBlock()
	conv.handleToolUse([]byte(`{"name":"tool","toolUseId":"toolu","input":"{\"a\":1}"}`))
	conv.handleToolUse([]byte(`{"name":"tool","toolUseId":"toolu","input":"{\"b\":2}","stop":false}`))
	conv.closeCurrentBlock()
	if !strings.Contains(rr.Body.String(), "input_json_delta") {
		t.Fatalf("tool delta missing:\n%s", rr.Body.String())
	}
}

func TestForwardHTTPLocalRoutesAndErrors(t *testing.T) {
	g := &KiroGateway{
		logger:    discardLogger(),
		headerCfg: defaultHeaderConfig(nil),
		tokenMgr:  newTokenManager(discardLogger(), defaultHeaderConfig(nil)),
		client:    &http.Client{},
	}
	account := &sdk.Account{ID: 1, Type: "api_key", Credentials: map[string]string{"kiro_api_key": "ksk"}}

	modelsRecorder := httptest.NewRecorder()
	modelsReq := &sdk.ForwardRequest{Account: account, Headers: http.Header{"X-Original-Path": []string{"/v1/models"}}, Writer: modelsRecorder}
	outcome, err := g.forwardHTTP(context.Background(), modelsReq, discardLogger())
	if err != nil || outcome.Kind != sdk.OutcomeSuccess || !strings.Contains(modelsRecorder.Body.String(), `"object":"list"`) {
		t.Fatalf("models route outcome=%+v err=%v body=%s", outcome, err, modelsRecorder.Body.String())
	}

	countRecorder := httptest.NewRecorder()
	countReq := &sdk.ForwardRequest{
		Account: account,
		Headers: http.Header{"X-Original-Path": []string{"/v1/messages/count_tokens"}},
		Writer:  countRecorder,
		Body:    []byte(`{"system":"sys","messages":[{"content":[{"type":"text","text":"hello"}]}],"tools":[{"name":"t"}]}`),
	}
	outcome, err = g.forwardHTTP(context.Background(), countReq, discardLogger())
	if err != nil || outcome.Kind != sdk.OutcomeSuccess || !strings.Contains(string(outcome.Upstream.Body), "input_tokens") {
		t.Fatalf("count route outcome=%+v err=%v", outcome, err)
	}
	if estimateInputTokens([]byte(`{}`)) != 1 {
		t.Fatal("empty token estimate should be at least one")
	}
	for _, size := range []int{520, 900, 1400, 3200, 4000} {
		body := []byte(`{"messages":[{"content":"` + strings.Repeat("a", size) + `"}]}`)
		if got := estimateInputTokens(body); got <= 0 {
			t.Fatalf("estimateInputTokens size %d = %d", size, got)
		}
	}
	if estimateCharUnits("a世") != 5 {
		t.Fatal("mixed char units should count non-ASCII as four")
	}
	if got := resolveRequestPath(&sdk.ForwardRequest{Headers: http.Header{"X-Original-Path": []string{"/custom"}}}); got != "/custom" {
		t.Fatalf("explicit path = %q", got)
	}
	if got := resolveRequestPath(&sdk.ForwardRequest{Body: []byte(`{"messages":[]}`)}); got != "/v1/messages" {
		t.Fatalf("body-inferred path = %q", got)
	}
	if got := resolveRequestPath(&sdk.ForwardRequest{}); got != "/v1/messages" {
		t.Fatalf("default path = %q", got)
	}

	unsupported := &sdk.ForwardRequest{Account: &sdk.Account{Type: "unknown", Credentials: map[string]string{}}, Body: []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`)}
	outcome, err = g.forwardHTTP(context.Background(), unsupported, discardLogger())
	if err != nil || outcome.Kind != sdk.OutcomeAccountDead {
		t.Fatalf("unsupported account outcome=%+v err=%v", outcome, err)
	}

	outcome, err = g.forwardAPIKey(context.Background(), &sdk.ForwardRequest{Account: &sdk.Account{Type: "api_key", Credentials: map[string]string{}}}, discardLogger())
	if err != nil || outcome.Kind != sdk.OutcomeAccountDead {
		t.Fatalf("missing api key outcome=%+v err=%v", outcome, err)
	}
}

func TestForwardOAuthAndAPIKeySuccessAndRetry(t *testing.T) {
	var upstreamCalls int
	g := &KiroGateway{
		logger:    discardLogger(),
		headerCfg: defaultHeaderConfig(nil),
		client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			upstreamCalls++
			if upstreamCalls == 1 {
				return httpResp(http.StatusForbidden, "bearer token is invalid"), nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(eventFrame("assistantResponseEvent", `{"content":"retried"}`))),
			}, nil
		})},
		tokenMgr: newTokenManager(discardLogger(), defaultHeaderConfig(nil)),
	}
	g.tokenMgr.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusOK, `{"accessToken":"fresh","refreshToken":"fresh-refresh","expiresIn":60}`), nil
	})}
	req := &sdk.ForwardRequest{
		Account: &sdk.Account{ID: 300, Type: "oauth", Credentials: map[string]string{
			"access_token":  "stale",
			"refresh_token": strings.Repeat("r", 120),
			"expires_at":    time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		}},
		Body: []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`),
	}
	outcome, err := g.forwardOAuth(context.Background(), req, discardLogger())
	if err != nil || outcome.Kind != sdk.OutcomeSuccess || outcome.UpdatedCredentials["access_token"] != "fresh" || upstreamCalls != 2 {
		t.Fatalf("oauth retry outcome=%+v err=%v upstreamCalls=%d", outcome, err, upstreamCalls)
	}

	outcome, err = g.forwardOAuth(context.Background(), &sdk.ForwardRequest{
		Account: &sdk.Account{ID: 301, Type: "oauth", Credentials: map[string]string{"refresh_token": "short"}},
		Body:    req.Body,
	}, discardLogger())
	if err != nil || outcome.Kind != sdk.OutcomeAccountDead {
		t.Fatalf("oauth dead refresh outcome=%+v err=%v", outcome, err)
	}

	outcome, err = g.forwardOAuth(context.Background(), &sdk.ForwardRequest{
		Account: &sdk.Account{ID: 302, Type: "oauth", Credentials: map[string]string{
			"refresh_token": strings.Repeat("r", 120),
			"expires_at":    time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		}},
		Body: req.Body,
	}, discardLogger())
	if err != nil || outcome.Kind != sdk.OutcomeAccountDead {
		t.Fatalf("oauth missing access outcome=%+v err=%v", outcome, err)
	}

	g.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(eventFrame("assistantResponseEvent", `{"content":"api ok"}`))),
		}, nil
	})}
	apiOutcome, err := g.forwardAPIKey(context.Background(), &sdk.ForwardRequest{
		Account: &sdk.Account{ID: 303, Type: "api_key", Credentials: map[string]string{"kiro_api_key": "ksk"}},
		Body:    req.Body,
	}, discardLogger())
	if err != nil || apiOutcome.Kind != sdk.OutcomeSuccess {
		t.Fatalf("api key success outcome=%+v err=%v", apiOutcome, err)
	}

	outcome, err = g.forwardHTTP(context.Background(), &sdk.ForwardRequest{
		Account: &sdk.Account{ID: 304, Type: "oauth", Credentials: map[string]string{
			"access_token":  "fresh",
			"refresh_token": strings.Repeat("r", 120),
			"expires_at":    time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		}},
		Body: req.Body,
	}, discardLogger())
	if err != nil || outcome.Kind != sdk.OutcomeSuccess {
		t.Fatalf("forwardHTTP oauth outcome=%+v err=%v", outcome, err)
	}
}

func TestDoForwardSuccessConvertAndHTTPFailures(t *testing.T) {
	var seenRequest *http.Request
	g := &KiroGateway{
		logger:    discardLogger(),
		headerCfg: defaultHeaderConfig(nil),
		client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenRequest = req
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(bytes.NewReader(eventFrame(
					"assistantResponseEvent",
					`{"content":"ok"}`,
				))),
			}, nil
		})},
	}
	req := &sdk.ForwardRequest{
		Account: &sdk.Account{ID: 2, Type: "api_key", Credentials: map[string]string{"kiro_api_key": "ksk", "region": "eu-west-1"}},
		Model:   "claude-sonnet-4-6",
		Body:    []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`),
	}
	outcome := g.doForward(context.Background(), req, discardLogger(), time.Now())
	if outcome.Kind != sdk.OutcomeSuccess || outcome.Usage == nil {
		t.Fatalf("doForward success outcome = %+v", outcome)
	}
	if seenRequest == nil || seenRequest.URL.Host != "q.eu-west-1.amazonaws.com" || seenRequest.Header.Get("tokentype") != "API_KEY" {
		t.Fatalf("upstream request not built correctly: %#v", seenRequest)
	}

	outcome = g.doForward(context.Background(), &sdk.ForwardRequest{
		Account: req.Account,
		Body:    []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`),
	}, discardLogger(), time.Now())
	if outcome.Kind != sdk.OutcomeClientError || outcome.Upstream.StatusCode != http.StatusBadRequest {
		t.Fatalf("convert error outcome = %+v", outcome)
	}

	g.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("network down")
	})}
	outcome = g.doForward(context.Background(), req, discardLogger(), time.Now())
	if outcome.Kind != sdk.OutcomeUpstreamTransient || !strings.Contains(outcome.Reason, "network down") {
		t.Fatalf("network error outcome = %+v", outcome)
	}

	g.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		resp := httpResp(http.StatusTooManyRequests, "slow down")
		resp.Header.Set("Retry-After", "2")
		return resp, nil
	})}
	outcome = g.doForward(context.Background(), req, discardLogger(), time.Now())
	if outcome.Kind != sdk.OutcomeAccountRateLimited || outcome.RetryAfter != 2*time.Second {
		t.Fatalf("HTTP failure outcome = %+v", outcome)
	}
}

func TestForwardAndValidateWrappers(t *testing.T) {
	g := &KiroGateway{
		logger:    discardLogger(),
		headerCfg: defaultHeaderConfig(nil),
		client:    &http.Client{},
	}
	req := &sdk.ForwardRequest{
		Account: &sdk.Account{ID: 3, Type: "api_key", Credentials: map[string]string{"kiro_api_key": "ksk"}},
		Headers: http.Header{"X-Original-Path": []string{"/v1/models"}},
	}
	outcome, err := g.Forward(context.Background(), req)
	if err != nil || outcome.Kind != sdk.OutcomeSuccess {
		t.Fatalf("Forward wrapper outcome=%+v err=%v", outcome, err)
	}
	if _, err := g.HandleWebSocket(context.Background(), nil); !errors.Is(err, sdk.ErrNotSupported) {
		t.Fatalf("HandleWebSocket err = %v", err)
	}

	g.tokenMgr = newTokenManager(discardLogger(), defaultHeaderConfig(nil))
	if err := g.ValidateAccount(context.Background(), map[string]string{"type": "unsupported"}); err == nil {
		t.Fatal("unsupported account type should fail validation")
	}
	if err := g.validateOAuth(context.Background(), &sdk.Account{Type: "oauth", Credentials: map[string]string{
		"refresh_token": strings.Repeat("r", 120),
		"access_token":  "access",
		"expires_at":    time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}}); err != nil {
		t.Fatalf("valid oauth should not refresh or fail: %v", err)
	}
	if err := g.validateOAuth(context.Background(), &sdk.Account{Type: "oauth", Credentials: map[string]string{
		"refresh_token": strings.Repeat("r", 120),
		"expires_at":    time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}}); err == nil {
		t.Fatal("oauth without access token should fail")
	}
	if err := g.validateOAuth(context.Background(), &sdk.Account{Type: "oauth", Credentials: map[string]string{
		"refresh_token": "short",
	}}); err == nil {
		t.Fatal("oauth refresh failure should fail validation")
	}
}

func TestWebSearchHelpersAndSyntheticResponses(t *testing.T) {
	if !hasWebSearchTool([]byte(`{"tools":[{"name":"web_search"}]}`)) {
		t.Fatal("web_search-only tool should be detected")
	}
	for _, body := range [][]byte{
		[]byte(`{}`),
		[]byte(`{"tools":[{"name":"web_search"},{"name":"other"}]}`),
		[]byte(`{"tools":[{"name":"other"}]}`),
	} {
		if hasWebSearchTool(body) {
			t.Fatalf("body should not be web_search-only: %s", string(body))
		}
	}
	if got := extractSearchQuery([]byte(`{"messages":[{"content":"Perform a web search for the query: golang"}]}`)); got != "golang" {
		t.Fatalf("string query = %q", got)
	}
	if got := extractSearchQuery([]byte(`{"messages":[{"content":[{"type":"text","text":"  rust  "}]}]}`)); got != "rust" {
		t.Fatalf("array query = %q", got)
	}
	if got := extractSearchQuery([]byte(`{"messages":[]}`)); got != "" {
		t.Fatalf("empty messages query = %q", got)
	}

	headers := buildMCPHeaders(&sdk.Account{Type: "api_key", Credentials: map[string]string{"kiro_api_key": "ksk", "profile_arn": "arn"}}, "us-west-2", "machine", defaultHeaderConfig(nil))
	if headers.Get("tokentype") != "API_KEY" || headers.Get("x-amzn-kiro-profile-arn") != "arn" {
		t.Fatalf("MCP headers mismatch: %v", headers)
	}

	published := int64(0)
	results := &webSearchResults{Results: []webSearchResult{{Title: "Title", URL: "https://example.com", Snippet: "Snippet", PublishedDate: &published}}}
	content := buildSearchResultContent(results)
	if len(content) != 1 || content[0]["page_age"] != "January 1, 1970" || content[0]["encrypted_content"] != "Snippet" {
		t.Fatalf("search result content = %#v", content)
	}
	if empty := buildSearchResultContent(&webSearchResults{}); len(empty) != 0 {
		t.Fatalf("empty result content = %#v", empty)
	}
	if summary := buildSearchSummary(&webSearchResults{}); summary != "No search results found." {
		t.Fatalf("empty summary = %q", summary)
	}
	if summary := buildSearchSummary(results); !strings.Contains(summary, "[Title](https://example.com)") {
		t.Fatalf("summary = %q", summary)
	}

	rr := httptest.NewRecorder()
	outcome := bufferWebSearchResponse(rr, results, "golang", "claude-sonnet-4-6", 10, time.Now())
	if outcome.Kind != sdk.OutcomeSuccess || rr.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("buffer web search outcome=%+v headers=%v", outcome, rr.Header())
	}
	rr = httptest.NewRecorder()
	outcome = streamWebSearchSSE(rr, results, "golang", "claude-sonnet-4-6", 10, time.Now())
	if outcome.Kind != sdk.OutcomeSuccess || !strings.Contains(rr.Body.String(), "server_tool_use") {
		t.Fatalf("stream web search outcome=%+v body=%s", outcome, rr.Body.String())
	}
}

func TestCallMCPAndHandleWebSearch(t *testing.T) {
	resultsText := `{"results":[{"title":"Go","url":"https://go.dev","snippet":"Docs"}],"query":"golang"}`
	mcpBody, _ := json.Marshal(mcpResponse{
		ID:      "id",
		JSONRPC: "2.0",
		Result:  &mcpResult{Content: []mcpContent{{Type: "text", Text: resultsText}}},
	})

	var callCount int
	g := &KiroGateway{
		logger:    discardLogger(),
		headerCfg: defaultHeaderConfig(nil),
		client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			callCount++
			if req.URL.Path != "/mcp" {
				t.Fatalf("MCP path = %q", req.URL.Path)
			}
			if req.Header.Get("Authorization") == "" {
				t.Fatal("missing MCP authorization header")
			}
			return httpResp(http.StatusOK, string(mcpBody)), nil
		})},
	}
	req := &sdk.ForwardRequest{
		Account: &sdk.Account{ID: 10, Type: "api_key", Credentials: map[string]string{"kiro_api_key": "ksk"}},
		Body: []byte(`{
			"model":"claude-sonnet-4-6",
			"tools":[{"name":"web_search"}],
			"messages":[{"role":"user","content":"Perform a web search for the query: golang"}]
		}`),
		Writer: httptest.NewRecorder(),
	}
	results, err := g.callMCP(context.Background(), req, "golang", discardLogger())
	if err != nil || len(results.Results) != 1 || results.Results[0].Title != "Go" {
		t.Fatalf("callMCP results=%+v err=%v", results, err)
	}
	outcome, err := g.handleWebSearch(context.Background(), req, discardLogger())
	if err != nil || outcome.Kind != sdk.OutcomeSuccess || callCount != 2 {
		t.Fatalf("handleWebSearch outcome=%+v err=%v callCount=%d", outcome, err, callCount)
	}

	outcome, err = g.handleWebSearch(context.Background(), &sdk.ForwardRequest{
		Account: &sdk.Account{Type: "api_key", Credentials: map[string]string{"kiro_api_key": "ksk"}},
		Body:    []byte(`{"messages":[]}`),
	}, discardLogger())
	if err != nil || outcome.Kind != sdk.OutcomeUpstreamTransient {
		t.Fatalf("missing query outcome=%+v err=%v", outcome, err)
	}
	outcome, err = g.handleWebSearch(context.Background(), &sdk.ForwardRequest{
		Account: &sdk.Account{Type: "api_key", Credentials: map[string]string{}},
		Body:    []byte(`{"messages":[{"content":"golang"}]}`),
	}, discardLogger())
	if err != nil || outcome.Kind != sdk.OutcomeAccountDead {
		t.Fatalf("missing api key outcome=%+v err=%v", outcome, err)
	}

	streamReq := *req
	streamReq.Stream = true
	streamReq.Writer = httptest.NewRecorder()
	outcome, err = g.forwardHTTP(context.Background(), &streamReq, discardLogger())
	if err != nil || outcome.Kind != sdk.OutcomeSuccess || !strings.Contains(streamReq.Writer.(*httptest.ResponseRecorder).Body.String(), "server_tool_use") {
		t.Fatalf("forwardHTTP web search stream outcome=%+v err=%v", outcome, err)
	}
}

func TestCallMCPErrorBranches(t *testing.T) {
	baseReq := &sdk.ForwardRequest{Account: &sdk.Account{Type: "api_key", Credentials: map[string]string{"kiro_api_key": "ksk"}}}
	tests := []struct {
		name       string
		status     int
		body       string
		wantErr    bool
		wantEmpty  bool
		transport  error
		readBroken bool
	}{
		{"transport", 0, "", true, false, errors.New("dial"), false},
		{"http error", http.StatusForbidden, "forbidden", true, false, nil, false},
		{"bad json", http.StatusOK, `{bad`, true, false, nil, false},
		{"mcp error", http.StatusOK, `{"error":{"code":1,"message":"bad"},"id":"x","jsonrpc":"2.0"}`, true, false, nil, false},
		{"nil result", http.StatusOK, `{"id":"x","jsonrpc":"2.0"}`, false, true, nil, false},
		{"empty result", http.StatusOK, `{"result":{"content":[]},"id":"x","jsonrpc":"2.0"}`, false, true, nil, false},
		{"unparseable text ignored", http.StatusOK, `{"result":{"content":[{"type":"text","text":"not json"}]},"id":"x","jsonrpc":"2.0"}`, false, true, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &KiroGateway{
				logger:    discardLogger(),
				headerCfg: defaultHeaderConfig(nil),
				client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if tt.transport != nil {
						return nil, tt.transport
					}
					return httpResp(tt.status, tt.body), nil
				})},
			}
			results, err := g.callMCP(context.Background(), baseReq, "q", discardLogger())
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantEmpty && (results == nil || len(results.Results) != 0) {
				t.Fatalf("expected empty results, got %+v", results)
			}
		})
	}
}

func TestCallMCPReadError(t *testing.T) {
	g := &KiroGateway{
		logger:    discardLogger(),
		headerCfg: defaultHeaderConfig(nil),
		client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: errReadCloser{}}, nil
		})},
	}
	_, err := g.callMCP(context.Background(), &sdk.ForwardRequest{
		Account: &sdk.Account{Type: "api_key", Credentials: map[string]string{"kiro_api_key": "ksk"}},
	}, "q", discardLogger())
	if err == nil || !strings.Contains(err.Error(), "read MCP response") {
		t.Fatalf("read error = %v", err)
	}
}

type errReadCloser struct{}

func (errReadCloser) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (errReadCloser) Close() error             { return nil }

func TestHandleWebSearchOAuthRetryAndNoAccess(t *testing.T) {
	mcpBody, _ := json.Marshal(mcpResponse{
		ID:      "id",
		JSONRPC: "2.0",
		Result:  &mcpResult{Content: []mcpContent{{Type: "text", Text: `{"results":[{"title":"Go","url":"https://go.dev"}]}`}}},
	})
	var mcpCalls int
	g := &KiroGateway{
		logger:    discardLogger(),
		headerCfg: defaultHeaderConfig(nil),
		tokenMgr:  newTokenManager(discardLogger(), defaultHeaderConfig(nil)),
		client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			mcpCalls++
			if mcpCalls == 1 {
				return httpResp(http.StatusForbidden, "invalid token"), nil
			}
			return httpResp(http.StatusOK, string(mcpBody)), nil
		})},
	}
	g.tokenMgr.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusOK, `{"accessToken":"fresh","refreshToken":"fresh-refresh","expiresIn":60}`), nil
	})}
	body := []byte(`{
		"model":"claude-sonnet-4-6",
		"tools":[{"name":"web_search"}],
		"messages":[{"role":"user","content":"golang"}]
	}`)
	outcome, err := g.handleWebSearch(context.Background(), &sdk.ForwardRequest{
		Account: &sdk.Account{ID: 400, Type: "oauth", Credentials: map[string]string{
			"access_token":  "stale",
			"refresh_token": strings.Repeat("r", 120),
			"expires_at":    time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		}},
		Body:   body,
		Writer: httptest.NewRecorder(),
	}, discardLogger())
	if err != nil || outcome.Kind != sdk.OutcomeSuccess || outcome.UpdatedCredentials["access_token"] != "fresh" || mcpCalls != 2 {
		t.Fatalf("oauth web search retry outcome=%+v err=%v calls=%d", outcome, err, mcpCalls)
	}

	outcome, err = g.handleWebSearch(context.Background(), &sdk.ForwardRequest{
		Account: &sdk.Account{ID: 401, Type: "oauth", Credentials: map[string]string{
			"refresh_token": strings.Repeat("r", 120),
			"expires_at":    time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		}},
		Body: body,
	}, discardLogger())
	if err != nil || outcome.Kind != sdk.OutcomeAccountDead {
		t.Fatalf("oauth web search missing access outcome=%+v err=%v", outcome, err)
	}

	outcome, err = g.handleWebSearch(context.Background(), &sdk.ForwardRequest{
		Account: &sdk.Account{ID: 402, Type: "oauth", Credentials: map[string]string{"refresh_token": "short"}},
		Body:    body,
	}, discardLogger())
	if err != nil || outcome.Kind != sdk.OutcomeAccountDead {
		t.Fatalf("oauth web search dead refresh outcome=%+v err=%v", outcome, err)
	}

	g.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusInternalServerError, "boom"), nil
	})}
	outcome, err = g.handleWebSearch(context.Background(), &sdk.ForwardRequest{
		Account: &sdk.Account{ID: 403, Type: "api_key", Credentials: map[string]string{"kiro_api_key": "ksk"}},
		Body:    body,
	}, discardLogger())
	if err != nil || outcome.Kind != sdk.OutcomeUpstreamTransient {
		t.Fatalf("web search MCP error outcome=%+v err=%v", outcome, err)
	}
}
