package gateway

import (
	"encoding/json"
	"math"
	"testing"

	sdk "github.com/DevilGenius/airgate-sdk/sdkgo"
)

func TestFillUsageCostReadsClaudeCacheCreationFromMetadata(t *testing.T) {
	usage := &sdk.Usage{
		Model:               "claude-sonnet-4-6",
		InputTokens:         1000,
		CachedInputTokens:   300,
		CacheCreationTokens: 90,
		OutputTokens:        2000,
	}
	setUsageMetadataInt(usage, usageMetaClaudeCacheCreation5mTokens, 40)
	setUsageMetadataInt(usage, usageMetaClaudeCacheCreation1hTokens, 50)

	fillUsageCost(usage)

	wantCacheCost := tokenCost(40, 3.75) + tokenCost(50, 6.0)
	if !near(usage.CacheCreationCost, wantCacheCost) {
		t.Fatalf("CacheCreationCost = %v, want %v", usage.CacheCreationCost, wantCacheCost)
	}
	if usage.CacheCreationPrice != 3.75 {
		t.Fatalf("CacheCreationPrice = %v, want 3.75", usage.CacheCreationPrice)
	}
	if usage.Metadata[usageMetaClaudeCacheCreation1hPrice] != "6" {
		t.Fatalf("metadata[%q] = %q, want 6", usageMetaClaudeCacheCreation1hPrice, usage.Metadata[usageMetaClaudeCacheCreation1hPrice])
	}
	if usage.Metadata[usageMetaClaudeCacheCreation5mTokens] != "40" || usage.Metadata[usageMetaClaudeCacheCreation1hTokens] != "50" {
		t.Fatalf("claude cache split metadata not preserved: %+v", usage.Metadata)
	}

	data, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["cache_creation_1h_price"]; ok {
		t.Fatalf("cache_creation_1h_price must not be a top-level Usage field: %s", string(data))
	}
	metadata, ok := payload["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata missing from usage JSON: %s", string(data))
	}
	if metadata[usageMetaClaudeCacheCreation1hPrice] != "6" {
		t.Fatalf("metadata[%q] = %v, want 6", usageMetaClaudeCacheCreation1hPrice, metadata[usageMetaClaudeCacheCreation1hPrice])
	}
}

func near(got, want float64) bool {
	return math.Abs(got-want) < 1e-12
}
