package gateway

import (
	"net/http"
	"testing"

	sdk "github.com/DouDOU-start/airgate-sdk/sdkgo"
)

func TestClassifyHTTPFailure_ModelUnsupportedBeatsForbidden(t *testing.T) {
	got := classifyHTTPFailure(http.StatusForbidden, `{"error":"model_not_supported"}`)
	if got != sdk.OutcomeClientError {
		t.Fatalf("classifyHTTPFailure() = %v，期望 OutcomeClientError", got)
	}
}
