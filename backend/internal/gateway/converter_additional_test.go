package gateway

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	sdk "github.com/DevilGenius/airgate-sdk/sdkgo"
	"github.com/tidwall/gjson"
)

func TestExtractSystemPromptVariants(t *testing.T) {
	if got := extractSystemPrompt(gjson.Parse(`{}`)); got != "" {
		t.Fatalf("missing system = %q", got)
	}
	if got := extractSystemPrompt(gjson.Parse(`{"system":"plain"}`)); got != "plain" {
		t.Fatalf("string system = %q", got)
	}
	body := `{
		"system": [
			{"type":"text","text":"first"},
			{"type":"text","text":"x-anthropic-billing-header: skip me"},
			{"type":"image","source":{"data":"ignored"}},
			{"type":"text","text":"second"}
		]
	}`
	if got := extractSystemPrompt(gjson.Parse(body)); got != "first\nsecond" {
		t.Fatalf("array system = %q", got)
	}
	if got := extractSystemPrompt(gjson.Parse(`{"system":123}`)); got != "" {
		t.Fatalf("numeric system = %q", got)
	}
}

func TestBuildThinkingPrefixDefaults(t *testing.T) {
	enabledDefault := buildThinkingPrefix(gjson.Parse(`{"thinking":{"type":"enabled","budget_tokens":0}}`))
	if !strings.Contains(enabledDefault, "102400") {
		t.Fatalf("enabled default budget = %q", enabledDefault)
	}
	adaptiveDefault := buildThinkingPrefix(gjson.Parse(`{"thinking":{"type":"adaptive"}}`))
	if !strings.Contains(adaptiveDefault, "medium") {
		t.Fatalf("adaptive default effort = %q", adaptiveDefault)
	}
	if got := buildThinkingPrefix(gjson.Parse(`{"thinking":{"type":"unknown"}}`)); got != "" {
		t.Fatalf("unknown thinking prefix = %q", got)
	}
}

func TestConvertToolsAdditionalBranches(t *testing.T) {
	result, nameMap := convertTools(nil)
	if result != nil || nameMap != nil {
		t.Fatalf("nil tools = %#v %#v", result, nameMap)
	}

	result, nameMap = convertTools([]gjson.Result{
		gjson.Parse(`{"type":"web_search_20250305","name":"web_search"}`),
	})
	if len(result) != 0 || len(nameMap) != 0 {
		t.Fatalf("server tools should be skipped: %#v %#v", result, nameMap)
	}

	result, nameMap = convertTools([]gjson.Result{
		gjson.Parse(`{"name":"no_schema","description":"No schema"}`),
	})
	if len(result) != 1 || len(nameMap) != 0 {
		t.Fatalf("default schema tool mismatch: %#v %#v", result, nameMap)
	}
	spec := result[0].(map[string]any)["toolSpecification"].(map[string]any)
	schema := spec["inputSchema"].(map[string]any)["json"].(json.RawMessage)
	if !gjson.GetBytes(schema, "type").Exists() || !gjson.GetBytes(schema, "properties").Exists() {
		t.Fatalf("default schema missing fields: %s", string(schema))
	}

	longMCP := "mcp__" + strings.Repeat("server_", 12) + "__short_tool"
	if got := shortenToolName(longMCP); got != "mcp__short_tool" {
		t.Fatalf("MCP shortened name = %q", got)
	}
	longFallback := strings.Repeat("z", 90)
	if got := shortenToolName(longFallback); len(got) > maxToolNameLen || !strings.HasPrefix(got, strings.Repeat("z", truncToolNameLen)+"_") {
		t.Fatalf("fallback shortened name = %q", got)
	}
}

func TestNormalizeSchemaAdditionalBranches(t *testing.T) {
	got := normalizeSchema(`{"$schema":"https://json-schema.org/draft/2020-12/schema","properties":{"x":{"type":"string"}}}`)
	if gjson.Get(got, "\\$schema").Exists() {
		t.Fatalf("$schema should be removed: %s", got)
	}
	if gjson.Get(got, "type").String() != "object" {
		t.Fatalf("missing type should default to object: %s", got)
	}

	got = normalizeSchema(`{"type":"","required":null}`)
	if gjson.Get(got, "type").String() != "object" || !gjson.Get(got, "properties").Exists() || !gjson.Get(got, "required").IsArray() {
		t.Fatalf("schema normalization incomplete: %s", got)
	}
}

func TestUserAndAssistantContentConversion(t *testing.T) {
	text, toolResults, images := extractUserContent(gjson.Parse(`"hello"`))
	if text != "hello" || toolResults != nil || images != nil {
		t.Fatalf("string content converted to %q %#v %#v", text, toolResults, images)
	}

	content := gjson.Parse(`[
		{"type":"text","text":"hello"},
		{"type":"image","source":{"base64":"abc","mime_type":"image/jpeg"}},
		{"type":"tool_result","tool_use_id":"toolu_ok","content":"done"},
		{"type":"tool_result","tool_use_id":"toolu_bad","is_error":true,"content":[
			{"type":"text","text":"failed"},
			{"type":"image","source":{"data":"xyz","media_type":"image/png"}}
		]}
	]`)
	text, toolResults, images = extractUserContent(content)
	if text != "hello" || len(toolResults) != 2 || len(images) != 2 {
		t.Fatalf("array content converted to text=%q toolResults=%#v images=%#v", text, toolResults, images)
	}
	if toolResults[1].(map[string]any)["status"] != "error" {
		t.Fatalf("error tool result not marked: %#v", toolResults[1])
	}
	if images[0].(map[string]any)["format"] != "jpeg" || images[1].(map[string]any)["format"] != "png" {
		t.Fatalf("image formats mismatch: %#v", images)
	}

	if img := convertImageBlock(gjson.Parse(`{"source":{}}`)); img != nil {
		t.Fatalf("empty image source should return nil: %#v", img)
	}
	if img := convertImageBlock(gjson.Parse(`{}`)); img != nil {
		t.Fatalf("missing image source should return nil: %#v", img)
	}
	if img := convertImageBlock(gjson.Parse(`{"source":{"data":"abc"}}`)); img["format"] != "png" {
		t.Fatalf("default image format = %#v", img)
	}

	userMsg := buildUserHistoryMessage(gjson.Parse(`{"content":[{"type":"tool_result","tool_use_id":"toolu","content":"done"}]}`), "kiro")
	user := userMsg["userInputMessage"].(map[string]any)
	if user["content"] != "Here are the tool results." {
		t.Fatalf("tool-only user content = %#v", user)
	}
	if _, ok := user["userInputMessageContext"]; !ok {
		t.Fatalf("tool-only user context missing: %#v", user)
	}
	imageOnly := buildUserHistoryMessage(gjson.Parse(`{"content":[{"type":"image","source":{"data":"abc"}}]}`), "kiro")
	if len(imageOnly["userInputMessage"].(map[string]any)["images"].([]any)) != 1 {
		t.Fatalf("image-only user message = %#v", imageOnly)
	}

	assistantString := buildAssistantHistoryMessage(gjson.Parse(`{"content":"plain"}`))
	if assistantString["assistantResponseMessage"].(map[string]any)["content"] != "plain" {
		t.Fatalf("assistant string mismatch: %#v", assistantString)
	}
	assistantArray := buildAssistantHistoryMessage(gjson.Parse(`{"content":[
		{"type":"text","text":"hello"},
		{"type":"thinking","thinking":"ignored"},
		{"type":"redacted_thinking","data":"ignored"},
		{"type":"tool_use","id":"toolu","name":"read","input":{"path":"/tmp"}}
	]}`))
	assistant := assistantArray["assistantResponseMessage"].(map[string]any)
	if assistant["content"] != "hello" || len(assistant["toolUses"].([]any)) != 1 {
		t.Fatalf("assistant array mismatch: %#v", assistant)
	}
	noInput := buildAssistantHistoryMessage(gjson.Parse(`{"content":[{"type":"tool_use","id":"toolu","name":"read"}]}`))
	input := noInput["assistantResponseMessage"].(map[string]any)["toolUses"].([]any)[0].(map[string]any)["input"].(json.RawMessage)
	if string(input) != "{}" {
		t.Fatalf("missing tool input = %s", string(input))
	}
}

func TestHistoryCurrentMessageAndOrphanCleanup(t *testing.T) {
	history := buildHistory("system", "<thinking_mode>enabled</thinking_mode>", []gjson.Result{
		gjson.Parse(`{"role":"user","content":"hi"}`),
		gjson.Parse(`{"role":"assistant","content":"hello"}`),
		gjson.Parse(`{"role":"ignored","content":"nope"}`),
	}, "kiro")
	if len(history) != 4 {
		t.Fatalf("history len = %d, history=%#v", len(history), history)
	}

	current := buildCurrentMessage(gjson.Parse(`{"content":[{"type":"tool_result","tool_use_id":"toolu","content":"done"}]}`), "kiro", []any{"tool"})
	user := current["userInputMessage"].(map[string]any)
	if user["content"] != "Here are the tool results." || len(user["images"].([]any)) != 0 {
		t.Fatalf("current tool-only message mismatch: %#v", user)
	}

	ids := extractToolResultIDs(gjson.Parse(`{"content":[{"type":"tool_result","tool_use_id":"a"},{"type":"text","text":"x"}]}`))
	if !ids["a"] || len(ids) != 1 {
		t.Fatalf("tool result ids = %#v", ids)
	}
	if ids := extractToolResultIDs(gjson.Parse(`{"content":"x"}`)); len(ids) != 0 {
		t.Fatalf("string content ids = %#v", ids)
	}

	rawEntry := "raw"
	entries := []any{
		rawEntry,
		map[string]any{"assistantResponseMessage": map[string]any{
			"content": "tools",
			"toolUses": []any{
				map[string]any{"toolUseId": "keep"},
				map[string]any{"toolUseId": "drop"},
				map[string]any{"toolUseId": "current"},
			},
		}},
		map[string]any{"userInputMessage": map[string]any{
			"content": "results",
			"userInputMessageContext": map[string]any{
				"toolResults": []any{
					map[string]any{"toolUseId": "keep"},
					map[string]any{"toolUseId": "orphan"},
				},
			},
		}},
		map[string]any{"userInputMessage": map[string]any{
			"content": "only orphan",
			"userInputMessageContext": map[string]any{
				"toolResults": []any{map[string]any{"toolUseId": "orphan2"}},
			},
		}},
	}
	cleaned := cleanOrphanToolPairs(entries, map[string]bool{"current": true})
	if cleaned[0] != rawEntry {
		t.Fatalf("raw entry was not preserved: %#v", cleaned[0])
	}
	uses := cleaned[1].(map[string]any)["assistantResponseMessage"].(map[string]any)["toolUses"].([]any)
	if len(uses) != 2 {
		t.Fatalf("valid assistant tool uses = %#v", uses)
	}
	ctx := cleaned[2].(map[string]any)["userInputMessage"].(map[string]any)["userInputMessageContext"].(map[string]any)
	if len(ctx["toolResults"].([]any)) != 1 {
		t.Fatalf("valid user tool results = %#v", ctx)
	}
	if _, ok := cleaned[3].(map[string]any)["userInputMessage"].(map[string]any)["userInputMessageContext"]; ok {
		t.Fatalf("empty context should be removed: %#v", cleaned[3])
	}

	if got := orEmptySlice(nil); len(got) != 0 {
		t.Fatalf("nil slice normalized to %#v", got)
	}
	nonNil := []any{"x"}
	if got := orEmptySlice(nonNil); len(got) != 1 || got[0] != "x" {
		t.Fatalf("non-nil slice changed: %#v", got)
	}
}

func TestConvertRequestErrorsAndComplexRequest(t *testing.T) {
	account := &sdk.Account{Type: "oauth", Credentials: map[string]string{}}
	if _, _, err := convertRequest([]byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`), account, convertConfig{}, slog.Default()); err == nil {
		t.Fatal("unsupported model should fail")
	}
	if _, _, err := convertRequest([]byte(`{"model":"claude-sonnet-4-6","messages":[]}`), account, convertConfig{}, slog.Default()); err == nil {
		t.Fatal("empty messages should fail")
	}
	if _, _, err := convertRequest([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"assistant","content":"prefill"}]}`), account, convertConfig{}, slog.Default()); err == nil {
		t.Fatal("assistant-only messages should fail")
	}

	longToolName := strings.Repeat("tool", 25)
	body := []byte(`{
		"model": "claude-opus-4-7",
		"metadata": {"user_id": "session_12345678-1234-1234-1234-123456789abc"},
		"system": [{"type":"text","text":"sys"}],
		"thinking": {"type":"adaptive"},
		"tools": [{"name":"` + longToolName + `","description":"Long","input_schema":{"$schema":"x","properties":null,"required":null}}],
		"messages": [
			{"role":"user","content":"first"},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"` + longToolName + `","input":{"x":1}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"done"},{"type":"text","text":"next"}]},
			{"role":"assistant","content":"prefill"},
			{"role":"user","content":"current"}
		]
	}`)
	result, convCtx, err := convertRequest(body, account, convertConfig{ProfileArn: "arn:profile"}, slog.Default())
	if err != nil {
		t.Fatalf("complex convertRequest failed: %v", err)
	}
	if convCtx.KiroModelID != "claude-opus-4.7" || convCtx.ContextWindow != 1_000_000 || len(convCtx.ToolNameMap) != 1 {
		t.Fatalf("convert context mismatch: %+v", convCtx)
	}
	parsed := gjson.ParseBytes(result)
	if parsed.Get("profileArn").String() != "arn:profile" {
		t.Fatalf("profileArn missing: %s", string(result))
	}
	if parsed.Get("conversationState.conversationId").String() != "12345678-1234-1234-1234-123456789abc" {
		t.Fatalf("conversation id mismatch: %s", string(result))
	}
	if parsed.Get("conversationState.currentMessage.userInputMessage.content").String() != "current" {
		t.Fatalf("current message mismatch: %s", string(result))
	}
	if !strings.Contains(parsed.Get("conversationState.history.0.userInputMessage.content").String(), "<thinking_mode>adaptive</thinking_mode>") {
		t.Fatalf("thinking prefix missing from history: %s", string(result))
	}
}
