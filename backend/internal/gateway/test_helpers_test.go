package gateway

import (
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	sdk "github.com/DevilGenius/airgate-sdk/sdkgo"
)

type testPluginConfig map[string]string

func (c testPluginConfig) GetString(key string) string { return c[key] }

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
	for k, v := range c {
		out[k] = v
	}
	return out
}

type testPluginContext struct {
	logger *slog.Logger
	config sdk.PluginConfig
}

func (c testPluginContext) Logger() *slog.Logger {
	if c.logger != nil {
		return c.logger
	}
	return discardLogger()
}

func (c testPluginContext) Config() sdk.PluginConfig { return c.config }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func decodeObject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("decode JSON object: %v\n%s", err, string(body))
	}
	return obj
}
