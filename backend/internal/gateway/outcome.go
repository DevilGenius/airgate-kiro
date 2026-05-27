package gateway

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/DouDOU-start/airgate-sdk/sdkgo"
)

const (
	usageCurrencyUSD = "USD"

	usageAttrModel = "model"

	usageMetricInputTokens           = "input_tokens"
	usageMetricCachedInputTokens     = "cached_input_tokens"
	usageMetricCacheCreationTokens   = "cache_creation_input_tokens"
	usageMetricCacheCreation5mTokens = "cache_creation_5m_input_tokens"
	usageMetricCacheCreation1hTokens = "cache_creation_1h_input_tokens"
	usageMetricOutputTokens          = "output_tokens"
	usageMetricTotalTokens           = "total_tokens"

	usageMetaClaudeCacheCreation5mTokens = "claude.cache_creation_5m_tokens"
	usageMetaClaudeCacheCreation1hTokens = "claude.cache_creation_1h_tokens"
	usageMetaClaudeCacheCreation1hPrice  = "claude.cache_creation_1h_price"
)

func successOutcome(statusCode int, body []byte, headers http.Header, usage *sdk.Usage) sdk.ForwardOutcome {
	return sdk.ForwardOutcome{
		Kind: sdk.OutcomeSuccess,
		Upstream: sdk.UpstreamResponse{
			StatusCode: statusCode,
			Headers:    headers,
			Body:       body,
		},
		Usage: usage,
	}
}

func failureOutcome(statusCode int, body []byte, headers http.Header, message string, retryAfter time.Duration) sdk.ForwardOutcome {
	kind := classifyHTTPFailure(statusCode, message)
	reason := message
	if reason != "" {
		reason = fmt.Sprintf("HTTP %d: %s", statusCode, message)
	}
	return sdk.ForwardOutcome{
		Kind: kind,
		Upstream: sdk.UpstreamResponse{
			StatusCode: statusCode,
			Headers:    headers,
			Body:       body,
		},
		Reason:     reason,
		RetryAfter: retryAfter,
	}
}

func transientOutcome(reason string) sdk.ForwardOutcome {
	return sdk.ForwardOutcome{
		Kind:     sdk.OutcomeUpstreamTransient,
		Upstream: sdk.UpstreamResponse{StatusCode: http.StatusBadGateway},
		Reason:   reason,
	}
}

func accountDeadOutcome(reason string) sdk.ForwardOutcome {
	return sdk.ForwardOutcome{
		Kind:     sdk.OutcomeAccountDead,
		Upstream: sdk.UpstreamResponse{StatusCode: http.StatusUnauthorized},
		Reason:   reason,
	}
}

func streamAbortedOutcome(statusCode int, reason string, usage *sdk.Usage) sdk.ForwardOutcome {
	return sdk.ForwardOutcome{
		Kind: sdk.OutcomeStreamAborted,
		Upstream: sdk.UpstreamResponse{
			StatusCode: statusCode,
		},
		Reason: reason,
		Usage:  usage,
	}
}

func newTokenUsage(modelID string, inputTokens, outputTokens, cachedInputTokens int, firstTokenMs int64) *sdk.Usage {
	usage := &sdk.Usage{
		Model:        modelID,
		Currency:     usageCurrencyUSD,
		FirstTokenMs: firstTokenMs,
	}
	setUsageTokens(usage, inputTokens, outputTokens, cachedInputTokens)
	return usage
}

func setUsageTokens(usage *sdk.Usage, inputTokens, outputTokens, cachedInputTokens int) {
	if usage == nil {
		return
	}
	usage.InputTokens = inputTokens
	usage.OutputTokens = outputTokens
	usage.CachedInputTokens = cachedInputTokens
}

func setUsageInputTokens(usage *sdk.Usage, inputTokens int) {
	setUsageTokens(
		usage,
		inputTokens,
		usageMetricInt(usage, usageMetricOutputTokens),
		usageMetricInt(usage, usageMetricCachedInputTokens),
	)
}

func addUsageOutputTokens(usage *sdk.Usage, delta int) {
	if delta <= 0 {
		return
	}
	setUsageTokens(
		usage,
		usageMetricInt(usage, usageMetricInputTokens),
		usageMetricInt(usage, usageMetricOutputTokens)+delta,
		usageMetricInt(usage, usageMetricCachedInputTokens),
	)
}

func usageMetricInt(usage *sdk.Usage, key string) int {
	return int(usageMetricValue(usage, key))
}

func usageMetricValue(usage *sdk.Usage, key string) float64 {
	if usage == nil {
		return 0
	}
	switch key {
	case usageMetricInputTokens:
		return float64(usage.InputTokens)
	case usageMetricCachedInputTokens:
		return float64(usage.CachedInputTokens)
	case usageMetricCacheCreationTokens:
		return float64(usage.CacheCreationTokens)
	case usageMetricCacheCreation5mTokens:
		return usageMetadataFloat(usage, usageMetaClaudeCacheCreation5mTokens)
	case usageMetricCacheCreation1hTokens:
		return usageMetadataFloat(usage, usageMetaClaudeCacheCreation1hTokens)
	case usageMetricOutputTokens:
		return float64(usage.OutputTokens)
	case usageMetricTotalTokens:
		return float64(usage.InputTokens + usage.CachedInputTokens + usage.CacheCreationTokens + usage.OutputTokens)
	}
	return 0
}

func setUsageMetadata(usage *sdk.Usage, key, value string) {
	if usage == nil {
		return
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	if usage.Metadata == nil {
		usage.Metadata = map[string]string{}
	}
	usage.Metadata[key] = value
}

func setUsageMetadataInt(usage *sdk.Usage, key string, value int) {
	if value <= 0 {
		return
	}
	setUsageMetadata(usage, key, strconv.Itoa(value))
}

func setUsageMetadataFloat(usage *sdk.Usage, key string, value float64) {
	if value <= 0 {
		return
	}
	setUsageMetadata(usage, key, strconv.FormatFloat(value, 'f', -1, 64))
}

func usageMetadataFloat(usage *sdk.Usage, key string) float64 {
	if usage == nil {
		return 0
	}
	raw := strings.TrimSpace(usage.Metadata[key])
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return value
}

func recomputeUsageAccountCost(usage *sdk.Usage) {
	if usage == nil {
		return
	}
	total := usage.InputCost + usage.OutputCost + usage.CachedInputCost + usage.CacheCreationCost
	usage.AccountCost = total
	usage.Currency = usageCurrencyUSD
	if total > 0 {
		usage.Summary = fmt.Sprintf("标准成本 $%.6f", total)
	}
}
