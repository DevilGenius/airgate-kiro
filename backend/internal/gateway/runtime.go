package gateway

import (
	"context"
	"net/http"
	"net/url"

	sdk "github.com/DevilGenius/airgate-sdk/sdkgo"
)

func (g *KiroGateway) DescribeRuntime() sdk.RuntimeSpec {
	return sdk.RuntimeSpec{Callbacks: []sdk.CallbackSpec{{Name: "oauth", Listen: "127.0.0.1:3128"}}}
}

// All OAuth semantics stay in the gateway. Core hosts and forwards a generic
// declared HTTP callback; it never interprets the state or authorization code.
func (g *KiroGateway) HandleRuntimeCallback(ctx context.Context, request sdk.CallbackRequest) (sdk.CallbackResponse, error) {
	if request.Name != "oauth" || request.Method != http.MethodGet {
		return sdk.CallbackResponse{Status: 404}, nil
	}
	query, err := url.ParseQuery(request.Query)
	state := query.Get("state")
	if err != nil || len(state) == 0 || len(state) > 256 {
		return sdk.CallbackResponse{Status: 400, Body: []byte("invalid state")}, nil
	}
	if g.oauthStore == nil {
		return sdk.CallbackResponse{Status: 503}, nil
	}
	if _, _, found := g.oauthStore.findByState(state); !found {
		return sdk.CallbackResponse{Status: 400, Body: []byte("unknown or expired state")}, nil
	}
	fullURL := kiroCallbackBaseURL + request.Path
	if request.Query != "" {
		fullURL += "?" + request.Query
	}
	if g.oauthStore.shared != nil {
		if _, err := g.oauthStore.shared.CompareAndSwap(ctx, "oauth:callback:"+state, "0", fullURL); err != nil {
			return sdk.CallbackResponse{}, err
		}
	} else if g.callbackLn != nil {
		g.callbackLn.captured.Store(state, fullURL)
	}
	return sdk.CallbackResponse{Status: 200, Headers: http.Header{"Content-Type": []string{"text/html; charset=utf-8"}}, Body: []byte(callbackSuccessHTML)}, nil
}
