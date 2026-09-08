package gateway

import (
	"context"
	"github.com/DevilGenius/airgate-sdk/devkit/testhost"
	sdk "github.com/DevilGenius/airgate-sdk/sdkgo"
	"net/http"
	"testing"
	"time"
)

func TestSharedKiroOAuthAndCallbackAcrossGenerations(t *testing.T) {
	host := &testhost.State{}
	a, b := &sdk.RuntimeStateClient{Host: host}, &sdk.RuntimeStateClient{Host: host}
	old, newer := &oauthSessionStore{shared: a}, &oauthSessionStore{shared: b}
	if err := old.put("session", &OAuthSession{State: "state", CodeVerifier: "test verifier", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	id, session, ok := newer.findByState("state")
	if !ok || id != "session" || session.CodeVerifier != "test verifier" {
		t.Fatalf("OAuth lost: %s %+v %t", id, session, ok)
	}
	if _, err := a.Update(context.Background(), "oauth:callback:state", func(string) (string, error) { return "http://localhost:3128/?state=state&code=test", nil }); err != nil {
		t.Fatal(err)
	}
	listener := &callbackListener{shared: b}
	value, found := listener.getResult("state")
	if !found || value == "" {
		t.Fatal("captured callback lost")
	}
	if _, found := listener.getResult("state"); found {
		t.Fatal("callback consumed twice")
	}
}

func TestKiroInterpretsStandardCallbackInsideGateway(t *testing.T) {
	shared := &sdk.RuntimeStateClient{Host: &testhost.State{}}
	g := &KiroGateway{oauthStore: &oauthSessionStore{shared: shared}}
	if err := g.oauthStore.put("session", &OAuthSession{State: "nonce", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	request := sdk.CallbackRequest{Name: "oauth", Method: http.MethodGet, Path: "/callback", Query: "state=nonce&code=example"}
	response, err := g.HandleRuntimeCallback(t.Context(), request)
	if err != nil || response.Status != 200 {
		t.Fatal(response, err)
	}
	value, found, err := shared.Take(t.Context(), "oauth:callback:nonce")
	if err != nil || !found || value != "http://localhost:3128/callback?state=nonce&code=example" {
		t.Fatal(value, found, err)
	}
	request.Query = "state=unknown"
	response, err = g.HandleRuntimeCallback(t.Context(), request)
	if err != nil || response.Status != 400 {
		t.Fatal("unknown OAuth state accepted", response, err)
	}
}
