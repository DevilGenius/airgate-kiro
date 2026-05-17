package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/DouDOU-start/airgate-sdk/sdkgo"
)

type testPluginContext struct {
	config sdk.PluginConfig
}

func (c testPluginContext) Logger() *slog.Logger {
	return slog.Default()
}

func (c testPluginContext) Config() sdk.PluginConfig {
	return c.config
}

type testPluginConfig map[string]string

func (c testPluginConfig) GetString(key string) string {
	return c[key]
}

func (c testPluginConfig) GetInt(key string) int {
	v, _ := strconv.Atoi(c[key])
	return v
}

func (c testPluginConfig) GetBool(key string) bool {
	v, _ := strconv.ParseBool(c[key])
	return v
}

func (c testPluginConfig) GetFloat64(key string) float64 {
	v, _ := strconv.ParseFloat(c[key], 64)
	return v
}

func (c testPluginConfig) GetDuration(key string) time.Duration {
	v, _ := time.ParseDuration(c[key])
	return v
}

func (c testPluginConfig) GetAll() map[string]string {
	out := make(map[string]string, len(c))
	for key, value := range c {
		out[key] = value
	}
	return out
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestHandleAccountQuota(t *testing.T) {
	g := &KiroGateway{
		headerCfg: defaultHeaderConfig(nil),
		tokenMgr:  newTokenManager(nil, defaultHeaderConfig(nil)),
		client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != "q.us-east-1.amazonaws.com" {
				t.Fatalf("host = %q, want q.us-east-1.amazonaws.com", req.URL.Host)
			}
			if req.Header.Get("Authorization") != "Bearer access-token" {
				t.Fatalf("Authorization = %q", req.Header.Get("Authorization"))
			}
			body := `{
				"nextDateReset": 1893456000,
				"subscriptionInfo": {"subscriptionTitle": "builder id pro"},
				"usageBreakdownList": [{
					"currentUsageWithPrecision": 25,
					"usageLimitWithPrecision": 100,
					"nextDateReset": 1893456000,
					"bonuses": [{"currentUsage": 5, "usageLimit": 10, "status": "ACTIVE"}],
					"freeTrialInfo": {
						"currentUsageWithPrecision": 2,
						"usageLimitWithPrecision": 8,
						"freeTrialStatus": "ACTIVE",
						"freeTrialExpiry": 1896048000
					}
				}]
			}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		})},
	}

	reqBody, _ := json.Marshal(map[string]any{
		"id": int64(42),
		"credentials": map[string]string{
			"access_token":  "access-token",
			"expires_at":    time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"email":         "dev@example.com",
			"refresh_token": "refresh-token",
		},
	})

	status, _, respBody, err := g.HandleRequest(context.Background(), http.MethodPost, "accounts/quota", "", nil, reqBody)
	if err != nil {
		t.Fatalf("HandleRequest returned error: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, respBody)
	}

	var resp struct {
		ExpiresAt string            `json:"expires_at"`
		Extra     map[string]string `json:"extra"`
	}
	if err := json.NewDecoder(bytes.NewReader(respBody)).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.ExpiresAt != "2030-01-01T00:00:00Z" {
		t.Fatalf("expires_at = %q", resp.ExpiresAt)
	}
	assertExtra := func(key, want string) {
		t.Helper()
		if got := resp.Extra[key]; got != want {
			t.Fatalf("extra[%s] = %q, want %q", key, got, want)
		}
	}
	assertExtra("subscription", "builder id pro")
	assertExtra("plan_type", "Builder Id Pro")
	assertExtra("email", "dev@example.com")
	assertExtra("quota_total", "118")
	assertExtra("quota_used", "32")
	assertExtra("quota_remaining", "86")
	assertExtra("quota_currency", "requests")
	assertExtra("access_token", "access-token")
}

func TestHandleAccountQuotaInvalidBody(t *testing.T) {
	g := &KiroGateway{}
	status, _, _, err := g.HandleRequest(context.Background(), http.MethodPost, "accounts/quota", "", nil, []byte(`{}`))
	if err != nil {
		t.Fatalf("HandleRequest returned error: %v", err)
	}
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestBuildUsageWindowsCanIgnoreLimit(t *testing.T) {
	g := &KiroGateway{
		ctx: testPluginContext{
			config: testPluginConfig{"ignore_usage_limit": "true"},
		},
	}

	windows := g.buildUsageWindows(&quotaInfo{Used: 180, Total: 100}, time.Now())
	if len(windows) != 1 {
		t.Fatalf("windows len = %d, want 1", len(windows))
	}
	if !windows[0].IgnoreLimit {
		t.Fatalf("IgnoreLimit = false, want true")
	}
	if windows[0].UsedPercent != 180 {
		t.Fatalf("UsedPercent = %v, want 180", windows[0].UsedPercent)
	}
}
