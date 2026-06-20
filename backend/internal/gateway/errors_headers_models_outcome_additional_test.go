package gateway

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	sdk "github.com/DevilGenius/airgate-sdk/sdkgo"
)

func TestClassifyHTTPFailureAdditionalCases(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		message string
		want    sdk.OutcomeKind
	}{
		{"rate limited", http.StatusTooManyRequests, "slow down", sdk.OutcomeAccountRateLimited},
		{"model unsupported", http.StatusBadRequest, "the model does not exist", sdk.OutcomeClientError},
		{"unauthorized", http.StatusUnauthorized, "bad token", sdk.OutcomeAccountDead},
		{"forbidden", http.StatusForbidden, "bad token", sdk.OutcomeAccountDead},
		{"monthly request count", http.StatusPaymentRequired, "MONTHLY_REQUEST_COUNT exceeded", sdk.OutcomeUpstreamTransient},
		{"disabled bad request", http.StatusBadRequest, "account suspended", sdk.OutcomeAccountDead},
		{"server error", http.StatusBadGateway, "upstream", sdk.OutcomeUpstreamTransient},
		{"plain client error", http.StatusConflict, "duplicate", sdk.OutcomeClientError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyHTTPFailure(tt.status, tt.message); got != tt.want {
				t.Fatalf("classifyHTTPFailure() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestErrorClassificationHelpers(t *testing.T) {
	modelTexts := map[string]bool{
		"":                              false,
		"model_not_found":               true,
		"unsupported_model":             true,
		"this model is not available":   true,
		"authentication is not found":   false,
		"invalid model family selected": true,
	}
	for text, want := range modelTexts {
		if got := isModelUnsupportedText(text); got != want {
			t.Fatalf("isModelUnsupportedText(%q) = %v, want %v", text, got, want)
		}
	}

	for _, text := range []string{"disabled", "DEACTIVATED", "account suspended"} {
		if !containsAccountDisabledKeyword(text) {
			t.Fatalf("containsAccountDisabledKeyword(%q) = false", text)
		}
	}

	tokenErrors := []error{
		errors.New("bearer token rejected"),
		errors.New("token is invalid"),
		errors.New("invalid token"),
		errors.New("HTTP 401"),
		errors.New("HTTP 403"),
	}
	for _, err := range tokenErrors {
		if !isTokenInvalidError(err) {
			t.Fatalf("isTokenInvalidError(%q) = false", err)
		}
	}

	nonRetryable := []error{
		errors.New("invalid_grant: invalid refresh token"),
		errors.New("expired_token"),
		errors.New("unauthorized_client"),
		errors.New("invalid_client"),
		errors.New("access_denied"),
	}
	for _, err := range nonRetryable {
		if !isNonRetryableRefreshError(err) {
			t.Fatalf("isNonRetryableRefreshError(%q) = false", err)
		}
	}
	if isNonRetryableRefreshError(errors.New("invalid_grant: slow down")) {
		t.Fatal("invalid_grant without invalid refresh token should be retryable")
	}
	if isNonRetryableRefreshError(errors.New("temporary network error")) {
		t.Fatal("temporary errors should be retryable")
	}

	dead := sdk.ForwardOutcome{Upstream: sdk.UpstreamResponse{StatusCode: http.StatusForbidden, Body: []byte("bearer token is invalid")}}
	if !isBearerTokenInvalidResponse(dead) {
		t.Fatal("expected bearer token invalid response")
	}
	otherStatus := sdk.ForwardOutcome{Upstream: sdk.UpstreamResponse{StatusCode: http.StatusBadRequest, Body: []byte("invalid token")}}
	if isBearerTokenInvalidResponse(otherStatus) {
		t.Fatal("bad request must not be treated as bearer token invalid")
	}
}

func TestInferAccountTypeRetryAfterAndTruncate(t *testing.T) {
	if got := inferAccountType(map[string]string{"type": "idc"}); got != "oauth" {
		t.Fatalf("idc should normalize to oauth, got %q", got)
	}
	if got := inferAccountType(map[string]string{"type": "api_key"}); got != "api_key" {
		t.Fatalf("explicit type = %q", got)
	}
	if got := inferAccountType(map[string]string{"kiro_api_key": "ksk"}); got != "api_key" {
		t.Fatalf("api key credentials inferred as %q", got)
	}
	if got := inferAccountType(map[string]string{}); got != "oauth" {
		t.Fatalf("default account type = %q", got)
	}

	if got := extractRetryAfter(http.Header{}); got != 0 {
		t.Fatalf("empty retry-after = %v", got)
	}
	if got := extractRetryAfter(http.Header{"Retry-After": []string{"3"}}); got != 3*time.Second {
		t.Fatalf("seconds retry-after = %v", got)
	}
	future := time.Now().Add(time.Minute).UTC().Format(http.TimeFormat)
	if got := extractRetryAfter(http.Header{"Retry-After": []string{future}}); got <= 0 {
		t.Fatalf("date retry-after should be positive, got %v", got)
	}
	past := time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)
	if got := extractRetryAfter(http.Header{"Retry-After": []string{past}}); got != 0 {
		t.Fatalf("past retry-after = %v", got)
	}
	if got := extractRetryAfter(http.Header{"Retry-After": []string{"not a date"}}); got != 0 {
		t.Fatalf("invalid retry-after = %v", got)
	}

	if got := truncateString("abc", 3); got != "abc" {
		t.Fatalf("truncate exact = %q", got)
	}
	if got := truncateString("abcdef", 3); got != "abc..." {
		t.Fatalf("truncate long = %q", got)
	}
}

func TestHeaderConfigAndRequestHeaders(t *testing.T) {
	cfg := defaultHeaderConfig(testPluginContext{config: testPluginConfig{
		"kiro_version": "1.2.3",
		"node_version": "24.0.0",
		"kiro_commit":  "abc123",
	}})
	if cfg.KiroVersion != "1.2.3" || cfg.NodeVersion != "24.0.0" || cfg.KiroCommit != "abc123" {
		t.Fatalf("config overrides not applied: %+v", cfg)
	}
	if cfg.SystemVersion == "" {
		t.Fatal("system version should keep default")
	}

	oauth := &sdk.Account{Type: "oauth", Credentials: map[string]string{"access_token": "access"}}
	h := buildKiroHeaders(oauth, "eu-west-1", "machine", cfg)
	if h.Get("Authorization") != "Bearer access" {
		t.Fatalf("oauth authorization = %q", h.Get("Authorization"))
	}
	if h.Get("Host") != "q.eu-west-1.amazonaws.com" {
		t.Fatalf("host = %q", h.Get("Host"))
	}
	if h.Get("x-amzn-kiro-commit") != "abc123" {
		t.Fatalf("commit header missing: %v", h)
	}
	if h.Get("tokentype") != "" {
		t.Fatalf("oauth tokentype should be empty, got %q", h.Get("tokentype"))
	}

	apiKey := &sdk.Account{Type: "api_key", Credentials: map[string]string{"kiro_api_key": "ksk_test"}}
	h = buildKiroHeaders(apiKey, "us-east-1", "machine", headerConfig{})
	if h.Get("Authorization") != "Bearer ksk_test" || h.Get("tokentype") != "API_KEY" {
		t.Fatalf("api key headers not set: %v", h)
	}
}

func TestResolveRegionAndMachineID(t *testing.T) {
	ctx := testPluginContext{config: testPluginConfig{"default_region": "ap-southeast-1"}}
	if got := resolveRegion(&sdk.Account{Credentials: map[string]string{"region": "eu-central-1"}}, ctx); got != "eu-central-1" {
		t.Fatalf("account region = %q", got)
	}
	if got := resolveRegion(&sdk.Account{Credentials: map[string]string{}}, ctx); got != "ap-southeast-1" {
		t.Fatalf("context region = %q", got)
	}
	if got := resolveRegion(&sdk.Account{Credentials: map[string]string{}}, nil); got != DefaultRegion {
		t.Fatalf("default region = %q", got)
	}

	if got := normalizeMachineID(""); len(got) != 64 {
		t.Fatalf("empty machine id length = %d", len(got))
	}
	if got := normalizeMachineID("ab-cd"); got != strings.Repeat("abcd", 16) {
		t.Fatalf("short machine id normalized to %q", got)
	}
	if got := normalizeMachineID(strings.Repeat("x", 70)); len(got) != 64 {
		t.Fatalf("long machine id length = %d", len(got))
	}

	explicit := resolveMachineID(&sdk.Account{Credentials: map[string]string{"machine_id": "aa"}})
	if explicit != strings.Repeat("aa", 32) {
		t.Fatalf("explicit machine id = %q", explicit)
	}

	account := &sdk.Account{ID: 990001, Type: "oauth", Credentials: map[string]string{"refresh_token": "first"}}
	first := resolveMachineID(account)
	account.Credentials["refresh_token"] = "second"
	if second := resolveMachineID(account); second != first {
		t.Fatalf("cached machine id changed: %q -> %q", first, second)
	}

	api := resolveMachineID(&sdk.Account{ID: 990002, Type: "api_key", Credentials: map[string]string{"kiro_api_key": "ksk"}})
	if len(api) != 64 {
		t.Fatalf("api key machine id length = %d", len(api))
	}
}

func TestPluginMetadataModelsAndRoutes(t *testing.T) {
	g := &KiroGateway{logger: discardLogger(), client: &http.Client{}}
	info := g.Info()
	if info.ID != PluginID || info.Type != sdk.PluginTypeGateway || info.Metadata["account.oauth_plans"] == "" {
		t.Fatalf("unexpected plugin info: %+v", info)
	}
	if len(PluginDependencies()) != 0 {
		t.Fatal("kiro plugin should not declare dependencies")
	}
	if g.Platform() != PluginPlatform {
		t.Fatalf("platform = %q", g.Platform())
	}
	if len(g.Models()) != len(kiroModels) {
		t.Fatalf("models len = %d, want %d", len(g.Models()), len(kiroModels))
	}
	if len(g.Routes()) != 3 {
		t.Fatalf("routes len = %d", len(g.Routes()))
	}
	if err := g.Start(nil); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	if got := MapToAnthropicModel("claude-opus-4.7"); got != "claude-opus-4-7" {
		t.Fatalf("MapToAnthropicModel known = %q", got)
	}
	if got := MapToAnthropicModel("custom"); got != "custom" {
		t.Fatalf("MapToAnthropicModel unknown = %q", got)
	}
	body := buildModelsResponse()
	obj := decodeObject(t, body)
	if obj["object"] != "list" {
		t.Fatalf("models response object = %v", obj["object"])
	}
}

func TestPricingAndUsageHelpers(t *testing.T) {
	if lookupPricing("claude-opus-4-7").OutputPrice != 25 {
		t.Fatal("exact opus pricing not found")
	}
	if lookupPricing("claude-haiku-4-5-20251001-extra").InputPrice != 1 {
		t.Fatal("prefix haiku pricing not found")
	}
	if lookupPricing("my-opus-model").InputPrice != 5 {
		t.Fatal("opus fallback pricing not found")
	}
	if lookupPricing("my-haiku-model").InputPrice != 1 {
		t.Fatal("haiku fallback pricing not found")
	}
	if lookupPricing("my-sonnet-model").InputPrice != 3 {
		t.Fatal("sonnet fallback pricing not found")
	}
	if lookupPricing("unknown").InputPrice != fallbackPricing.InputPrice {
		t.Fatal("default fallback pricing not used")
	}
	if tokenCost(0, 10) != 0 || tokenCost(10, 0) != 0 {
		t.Fatal("zero tokenCost should be zero")
	}

	usage := &sdk.Usage{
		Model:               "claude-sonnet-4-6",
		InputTokens:         1000,
		OutputTokens:        2000,
		CachedInputTokens:   300,
		CacheCreationTokens: 100,
	}
	setUsageMetadataInt(usage, usageMetaClaudeCacheCreation5mTokens, 30)
	setUsageMetadataInt(usage, usageMetaClaudeCacheCreation1hTokens, 40)
	fillUsageCost(usage)
	if usage.InputPrice != 3 || usage.OutputPrice != 15 || usage.Currency != usageCurrencyUSD {
		t.Fatalf("usage prices not filled: %+v", usage)
	}
	if usage.AccountCost <= 0 || usage.Summary == "" {
		t.Fatalf("usage cost not recomputed: %+v", usage)
	}

	fillUsageCost(nil)
	fillUsageCost(&sdk.Usage{})
}

func TestOutcomeHelpers(t *testing.T) {
	headers := http.Header{"X-Test": []string{"yes"}}
	usage := &sdk.Usage{Model: "m"}
	success := successOutcome(http.StatusCreated, []byte("ok"), headers, usage)
	if success.Kind != sdk.OutcomeSuccess || success.Upstream.StatusCode != http.StatusCreated || success.Usage != usage {
		t.Fatalf("success outcome mismatch: %+v", success)
	}

	failure := failureOutcome(http.StatusTooManyRequests, []byte("slow"), headers, "slow", 2*time.Second)
	if failure.Kind != sdk.OutcomeAccountRateLimited || failure.Reason != "HTTP 429: slow" || failure.RetryAfter != 2*time.Second {
		t.Fatalf("failure outcome mismatch: %+v", failure)
	}
	emptyReason := failureOutcome(http.StatusBadRequest, nil, nil, "", 0)
	if emptyReason.Reason != "" {
		t.Fatalf("empty failure reason = %q", emptyReason.Reason)
	}
	if transientOutcome("network").Kind != sdk.OutcomeUpstreamTransient {
		t.Fatal("transient outcome kind mismatch")
	}
	if accountDeadOutcome("dead").Kind != sdk.OutcomeAccountDead {
		t.Fatal("account dead outcome kind mismatch")
	}
	aborted := streamAbortedOutcome(http.StatusOK, "cancelled", usage)
	if aborted.Kind != sdk.OutcomeStreamAborted || aborted.Usage != usage {
		t.Fatalf("stream aborted outcome mismatch: %+v", aborted)
	}

	tu := newTokenUsage("model", 10, 20, 3, 7)
	if tu.Model != "model" || tu.InputTokens != 10 || tu.OutputTokens != 20 || tu.CachedInputTokens != 3 || tu.FirstTokenMs != 7 {
		t.Fatalf("token usage mismatch: %+v", tu)
	}
	setUsageTokens(nil, 1, 2, 3)
	setUsageInputTokens(tu, 11)
	addUsageOutputTokens(tu, 0)
	addUsageOutputTokens(tu, 4)
	if tu.InputTokens != 11 || tu.OutputTokens != 24 {
		t.Fatalf("usage token mutation mismatch: %+v", tu)
	}

	tu.CacheCreationTokens = 5
	setUsageMetadata(tu, "blank", " ")
	setUsageMetadata(nil, "nil", "value")
	setUsageMetadata(tu, "kept", " value ")
	setUsageMetadataInt(tu, "non_positive", 0)
	setUsageMetadataFloat(tu, "float_zero", 0)
	setUsageMetadataFloat(tu, "float", 1.25)
	tu.Metadata["bad_float"] = "x"
	if tu.Metadata["kept"] != "value" {
		t.Fatalf("metadata value not trimmed: %+v", tu.Metadata)
	}
	delete(tu.Metadata, "missing")
	if usageMetricValue(tu, usageMetricTotalTokens) != 43 {
		t.Fatalf("total tokens = %v", usageMetricValue(tu, usageMetricTotalTokens))
	}
	if usageMetadataFloat(nil, "float") != 0 || usageMetadataFloat(tu, "float") != 1.25 || usageMetadataFloat(tu, "bad_float") != 0 || usageMetadataFloat(tu, "missing") != 0 {
		t.Fatalf("metadata float parsing mismatch: %+v", tu.Metadata)
	}
	if usageMetricValue(nil, usageMetricInputTokens) != 0 || usageMetricValue(tu, "unknown") != 0 {
		t.Fatal("unknown or nil usage metric should be zero")
	}

	recomputeUsageAccountCost(nil)
}

func TestPromptCacheSimulation(t *testing.T) {
	tracker := &promptCacheTracker{cache: map[string]*cacheEntry{}}
	if tracker.track("sys", "tools") {
		t.Fatal("first cache track should miss")
	}
	if !tracker.track("sys", "tools") {
		t.Fatal("second cache track should hit")
	}
	tracker.cache["old"] = &cacheEntry{lastHitAt: time.Now().Add(-cacheTTL * 3)}
	tracker.cleanupLocked(time.Now())
	if _, ok := tracker.cache["old"]; ok {
		t.Fatal("old cache entry was not cleaned")
	}

	if nonCached, cached := applyCacheSimulation(0, true); nonCached != 0 || cached != 0 {
		t.Fatalf("zero cache simulation = %d/%d", nonCached, cached)
	}
	if nonCached, cached := applyCacheSimulation(100, false); nonCached != 100 || cached != 0 {
		t.Fatalf("miss cache simulation = %d/%d", nonCached, cached)
	}
	if nonCached, cached := applyCacheSimulation(100, true); nonCached != 10 || cached != 90 {
		t.Fatalf("hit cache simulation = %d/%d", nonCached, cached)
	}

	oldGlobal := globalCacheTracker
	globalCacheTracker = &promptCacheTracker{cache: map[string]*cacheEntry{}}
	t.Cleanup(func() { globalCacheTracker = oldGlobal })

	applyCacheToUsage(nil, &ConvertContext{})
	applyCacheToUsage(&sdk.Usage{InputTokens: 10}, nil)
	usage := &sdk.Usage{InputTokens: 100, OutputTokens: 5}
	convCtx := &ConvertContext{SystemPrompt: "sys", ToolsJSON: "tools"}
	applyCacheToUsage(usage, convCtx)
	if usage.InputTokens != 100 || usage.CachedInputTokens != 0 {
		t.Fatalf("first usage cache application = %+v", usage)
	}
	applyCacheToUsage(usage, convCtx)
	if usage.InputTokens != 10 || usage.CachedInputTokens != 90 {
		t.Fatalf("second usage cache application = %+v", usage)
	}
}
