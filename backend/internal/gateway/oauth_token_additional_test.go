package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	sdk "github.com/DevilGenius/airgate-sdk/sdkgo"
)

func TestOAuthSessionStoreLifecycle(t *testing.T) {
	store := newOAuthSessionStore()
	store.sessions["old"] = &OAuthSession{State: "old", CreatedAt: time.Now().Add(-oauthSessionTTL - time.Minute)}
	store.put("fresh", &OAuthSession{State: "state", CreatedAt: time.Now()})
	if _, ok := store.get("old"); ok {
		t.Fatal("old session should be expired")
	}
	if sess, ok := store.get("fresh"); !ok || sess.State != "state" {
		t.Fatalf("fresh session lookup = %+v %v", sess, ok)
	}
	id, sess, ok := store.findByState("state")
	if !ok || id != "fresh" || sess.State != "state" {
		t.Fatalf("findByState = %q %+v %v", id, sess, ok)
	}
	store.remove("fresh")
	if _, ok := store.get("fresh"); ok {
		t.Fatal("removed session should be gone")
	}

	ctx, cancel := context.WithCancel(context.Background())
	store.startCleanup(ctx)
	cancel()
}

func TestGenerateAuthURLNormalizeCallbackAndJWT(t *testing.T) {
	store := newOAuthSessionStore()
	resp, err := generateAuthURL(store)
	if err != nil {
		t.Fatalf("generateAuthURL: %v", err)
	}
	if resp.SessionID == "" || resp.CallbackURL != kiroCallbackBaseURL {
		t.Fatalf("auth URL response mismatch: %+v", resp)
	}
	parsed, err := url.Parse(resp.AuthURL)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	state := parsed.Query().Get("state")
	if state == "" || parsed.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("auth URL query mismatch: %s", resp.AuthURL)
	}
	if _, _, ok := store.findByState(state); !ok {
		t.Fatal("generated state was not stored")
	}

	if normalizeCallbackURL(" http://x/cb ") != "http://x/cb" {
		t.Fatal("absolute callback URL should be trimmed")
	}
	if got := normalizeCallbackURL("/oauth/callback?code=1"); got != kiroCallbackBaseURL+"/oauth/callback?code=1" {
		t.Fatalf("path callback = %q", got)
	}
	if got := normalizeCallbackURL("?code=1"); got != kiroCallbackBaseURL+"/oauth/callback?code=1" {
		t.Fatalf("query callback = %q", got)
	}
	if got := normalizeCallbackURL("not a url"); got != "not a url" {
		t.Fatalf("raw callback = %q", got)
	}

	token := jwtWithClaims(map[string]any{"preferred_username": "dev@example.com"})
	if got := extractEmailFromJWT(token); got != "dev@example.com" {
		t.Fatalf("email from jwt = %q", got)
	}
	if got := extractEmailFromJWT("not.jwt"); got != "" {
		t.Fatalf("invalid jwt email = %q", got)
	}
	if got := extractEmailFromJWT("header.@@@.sig"); got != "" {
		t.Fatalf("bad base64 jwt email = %q", got)
	}
	noEmail := jwtWithClaims(map[string]any{"sub": "123"})
	if got := extractEmailFromJWT(noEmail); got != "" {
		t.Fatalf("jwt without email = %q", got)
	}
	if got := resolveAuthMethod("builderid", &kiroTokenExchangeResponse{}); got != "oauth" {
		t.Fatalf("auth method = %q", got)
	}
	if got, err := randomBase64URL(16); err != nil || got == "" {
		t.Fatalf("randomBase64URL = %q %v", got, err)
	}
	verifier := "verifier"
	sum := sha256.Sum256([]byte(verifier))
	wantChallenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if got := computeS256Challenge(verifier); got != wantChallenge {
		t.Fatalf("challenge = %q, want %q", got, wantChallenge)
	}
}

func TestCallbackListenerDirectHandling(t *testing.T) {
	listener := newCallbackListener(discardLogger())
	if listener.isRunning() {
		t.Fatal("new listener should not be running")
	}
	listener.stop()

	rr := httptest.NewRecorder()
	listener.handleCallback(rr, httptest.NewRequest(http.MethodGet, "/oauth/callback", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing state status = %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	listener.handleCallback(rr, httptest.NewRequest(http.MethodGet, "/oauth/callback?state=abcdef123456&code=c", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Authorization Complete") {
		t.Fatalf("callback status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got, ok := listener.getResult("abcdef123456"); !ok || !strings.Contains(got, "code=c") {
		t.Fatalf("captured callback = %q %v", got, ok)
	}
	if _, ok := listener.getResult("abcdef123456"); ok {
		t.Fatal("getResult should delete captured callback")
	}
}

func TestExchangeCodeForTokenResponseShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"direct", `{"accessToken":"a","refreshToken":"r","profileArn":"arn","expiresIn":60,"email":"dev@example.com"}`, "a"},
		{"wrapped", `{"data":{"accessToken":"wrapped","refreshToken":"r"}}`, "wrapped"},
		{"snake", `{"access_token":"snake","refresh_token":"r","profile_arn":"arn","expires_in":60}`, "snake"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != kiroTokenExchangeURL {
					t.Fatalf("token URL = %q", req.URL.String())
				}
				return httpResp(http.StatusOK, tt.body), nil
			})}
			resp, err := exchangeCodeForToken(context.Background(), "code", "verifier", kiroCallbackBaseURL, client)
			if err != nil || resp.AccessToken != tt.want {
				t.Fatalf("token resp=%+v err=%v", resp, err)
			}
		})
	}

	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusBadRequest, "bad"), nil
	})}
	if _, err := exchangeCodeForToken(context.Background(), "code", "verifier", kiroCallbackBaseURL, client); err == nil {
		t.Fatal("non-200 token exchange should fail")
	}
	client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("dial")
	})}
	if _, err := exchangeCodeForToken(context.Background(), "code", "verifier", kiroCallbackBaseURL, client); err == nil {
		t.Fatal("transport token exchange should fail")
	}
	client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusOK, `{"data":{}}`), nil
	})}
	if _, err := exchangeCodeForToken(context.Background(), "code", "verifier", kiroCallbackBaseURL, client); err == nil {
		t.Fatal("unparseable token exchange should fail")
	}
}

func TestExchangeCallbackByURLSocialAndErrors(t *testing.T) {
	store := newOAuthSessionStore()
	store.put("sess", &OAuthSession{State: "state", CodeVerifier: "verifier", CreatedAt: time.Now()})
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusOK, `{"accessToken":"`+jwtWithClaims(map[string]any{"email": "jwt@example.com"})+`","refreshToken":"refresh","expiresIn":60}`), nil
	})}
	resp, err := exchangeCallbackByURL(context.Background(), store, "?state=state&code=code&login_option=social", client)
	if err != nil {
		t.Fatalf("exchangeCallbackByURL social: %v", err)
	}
	if resp.Credentials["access_token"] == "" || resp.Email != "jwt@example.com" || resp.Credentials["auth_method"] != "oauth" {
		t.Fatalf("social exchange response = %+v", resp)
	}

	errorCases := []string{
		"?error=access_denied&error_description=no",
		"?code=missing_state",
		"?state=missing&code=code",
	}
	for _, raw := range errorCases {
		if _, err := exchangeCallbackByURL(context.Background(), newOAuthSessionStore(), raw, client); err == nil {
			t.Fatalf("callback %q should fail", raw)
		}
	}

	store = newOAuthSessionStore()
	store.put("sess", &OAuthSession{State: "state", CreatedAt: time.Now()})
	if _, err := exchangeCallbackByURL(context.Background(), store, "?state=state&login_option=external_idp", client); err == nil {
		t.Fatal("external_idp without code should fail")
	}

	store = newOAuthSessionStore()
	store.put("sess", &OAuthSession{State: "state", CreatedAt: time.Now()})
	if _, err := exchangeCallbackByURL(context.Background(), store, "?state=state", client); err == nil {
		t.Fatal("callback without code should fail")
	}

	store = newOAuthSessionStore()
	store.put("idc", &OAuthSession{State: "idc-state", ClientID: "client", ClientSecret: "secret", IDCRegion: "us-east-1", CodeVerifier: "verifier", CreatedAt: time.Now()})
	idcClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusOK, `{"accessToken":"idc","refreshToken":"refresh","expiresIn":60}`), nil
	})}
	idcResp, err := exchangeCallbackByURL(context.Background(), store, "?state=idc-state&code=code", idcClient)
	if err != nil || idcResp.Credentials["access_token"] != "idc" {
		t.Fatalf("idc callback resp=%+v err=%v", idcResp, err)
	}
}

func TestBuilderIDAndDeviceFlows(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/client/register":
			return httpResp(http.StatusOK, `{"clientId":"client","clientSecret":"secret"}`), nil
		case "/device_authorization":
			return httpResp(http.StatusOK, `{"deviceCode":"device","userCode":"user","verificationUri":"https://verify","verificationUriComplete":"https://verify?user_code=user"}`), nil
		default:
			t.Fatalf("unexpected path %q", req.URL.Path)
			return nil, nil
		}
	})}
	store := newOAuthSessionStore()
	resp, err := startBuilderIDContinuation(context.Background(), store, url.Values{"issuer_url": []string{"https://issuer"}, "idc_region": []string{"us-west-2"}}, client)
	if err != nil {
		t.Fatalf("startBuilderIDContinuation: %v", err)
	}
	if !resp.Continuation || resp.DeviceSessionID == "" || resp.VerificationURI != "https://verify?user_code=user" {
		t.Fatalf("builder continuation response = %+v", resp)
	}
	if _, ok := store.get(resp.DeviceSessionID); !ok {
		t.Fatal("device session not stored")
	}
	if _, err := startBuilderIDContinuation(context.Background(), store, url.Values{}, client); err == nil {
		t.Fatal("missing issuer should fail")
	}
	resp, err = startBuilderIDContinuation(context.Background(), newOAuthSessionStore(), url.Values{"issuer_url": []string{"https://issuer"}}, client)
	if err != nil || !strings.Contains(resp.DeviceSessionID, "idc-device-") {
		t.Fatalf("builder default region resp=%+v err=%v", resp, err)
	}

	if _, _, err := registerIDCClient(context.Background(), "us-east-1", "issuer", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusOK, `{}`), nil
	})}); err == nil {
		t.Fatal("empty client id should fail")
	}
	if _, _, err := registerIDCClient(context.Background(), "us-east-1", "issuer", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("dial")
	})}); err == nil {
		t.Fatal("IDC registration transport error should fail")
	}
	if _, _, err := registerIDCClient(context.Background(), "us-east-1", "issuer", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusOK, `{bad`), nil
	})}); err == nil {
		t.Fatal("IDC registration parse error should fail")
	}
	if _, err := startDeviceAuthorization(context.Background(), "us-east-1", "client", "secret", "issuer", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusInternalServerError, "boom"), nil
	})}); err == nil {
		t.Fatal("device authorization HTTP error should fail")
	}
	if _, err := startDeviceAuthorization(context.Background(), "us-east-1", "client", "secret", "issuer", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("dial")
	})}); err == nil {
		t.Fatal("device authorization transport error should fail")
	}
	if _, err := startDeviceAuthorization(context.Background(), "us-east-1", "client", "secret", "issuer", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusOK, `{bad`), nil
	})}); err == nil {
		t.Fatal("device authorization parse error should fail")
	}
}

func TestPollDeviceTokenAndIDCExchange(t *testing.T) {
	store := newOAuthSessionStore()
	if _, err := pollDeviceToken(context.Background(), store, "missing", &http.Client{}); err == nil {
		t.Fatal("missing device session should fail")
	}
	store.put("not-device", &OAuthSession{CreatedAt: time.Now()})
	if _, err := pollDeviceToken(context.Background(), store, "not-device", &http.Client{}); err == nil {
		t.Fatal("non-device session should fail")
	}

	store.put("pending", &OAuthSession{CreatedAt: time.Now(), DeviceCode: "device", ClientID: "client", ClientSecret: "secret", IDCRegion: "us-east-1"})
	pendingClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusBadRequest, `{"error":"authorization_pending"}`), nil
	})}
	if _, err := pollDeviceToken(context.Background(), store, "pending", pendingClient); err == nil || !strings.Contains(err.Error(), "请先") {
		t.Fatalf("pending device token err = %v", err)
	}

	store.put("done", &OAuthSession{CreatedAt: time.Now(), DeviceCode: "device", ClientID: "client", ClientSecret: "secret", IDCRegion: "us-east-1"})
	successClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusOK, `{"accessToken":"`+jwtWithClaims(map[string]any{"email": "device@example.com"})+`","refreshToken":"refresh","expiresIn":60}`), nil
	})}
	resp, err := pollDeviceToken(context.Background(), store, "done", successClient)
	if err != nil || resp.Email != "device@example.com" || resp.Credentials["client_id"] != "client" {
		t.Fatalf("device token resp=%+v err=%v", resp, err)
	}
	if _, ok := store.get("done"); ok {
		t.Fatal("completed device session should be removed")
	}

	sess := &OAuthSession{ClientID: "client", ClientSecret: "secret", IDCRegion: "us-east-1", CodeVerifier: "verifier"}
	resp, err = exchangeIDCCode(context.Background(), "code", sess, successClient)
	if err != nil || resp.Credentials["access_token"] == "" {
		t.Fatalf("IDC code exchange resp=%+v err=%v", resp, err)
	}

	badClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusOK, `{}`), nil
	})}
	parseClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusOK, `{bad`), nil
	})}
	httpErrorClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusInternalServerError, "boom"), nil
	})}
	if _, err := exchangeIDCCode(context.Background(), "code", sess, parseClient); err == nil {
		t.Fatal("IDC parse error should fail")
	}
	if _, err := exchangeIDCCode(context.Background(), "code", sess, badClient); err == nil {
		t.Fatal("IDC empty access token should fail")
	}
	if _, err := exchangeIDCCode(context.Background(), "code", sess, httpErrorClient); err == nil {
		t.Fatal("IDC HTTP error should fail")
	}
	transportClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("dial")
	})}
	if _, err := exchangeIDCCode(context.Background(), "code", sess, transportClient); err == nil {
		t.Fatal("IDC transport error should fail")
	}
	if _, err := pollDeviceToken(context.Background(), storeWithDeviceSession("http-error"), "http-error", httpErrorClient); err == nil {
		t.Fatal("device token HTTP error should fail")
	}
	if _, err := pollDeviceToken(context.Background(), storeWithDeviceSession("empty"), "empty", badClient); err == nil {
		t.Fatal("device token empty access token should fail")
	}
}

func TestTokenManagerValidationRefreshAndReuse(t *testing.T) {
	if err := validateRefreshToken(""); err == nil {
		t.Fatal("empty refresh token should fail")
	}
	if err := validateRefreshToken("short"); err == nil {
		t.Fatal("short refresh token should fail")
	}
	if err := validateRefreshToken(strings.Repeat("r", 50) + "..." + strings.Repeat("r", 50)); err == nil {
		t.Fatal("truncated refresh token should fail")
	}
	if err := validateRefreshToken(strings.Repeat("r", 120)); err != nil {
		t.Fatalf("valid refresh token failed: %v", err)
	}
	if got := resolveAuthRegion(&sdk.Account{Credentials: map[string]string{"auth_region": "auth", "region": "region"}}); got != "auth" {
		t.Fatalf("auth region = %q", got)
	}
	if got := resolveAuthRegion(&sdk.Account{Credentials: map[string]string{"region": "region"}}); got != "region" {
		t.Fatalf("region fallback = %q", got)
	}
	if got := resolveAuthRegion(&sdk.Account{Credentials: map[string]string{}}); got != DefaultRegion {
		t.Fatalf("default auth region = %q", got)
	}

	if resp, err := parseSocialRefreshResponse([]byte(`{"accessToken":"direct"}`)); err != nil || resp.AccessToken != "direct" {
		t.Fatalf("direct social parse resp=%+v err=%v", resp, err)
	}
	if resp, err := parseSocialRefreshResponse([]byte(`{"data":{"accessToken":"wrapped"}}`)); err != nil || resp.AccessToken != "wrapped" {
		t.Fatalf("wrapped social parse resp=%+v err=%v", resp, err)
	}
	if _, err := parseSocialRefreshResponse([]byte(`{}`)); err == nil {
		t.Fatal("empty social refresh response should fail")
	}

	mgr := newTokenManager(discardLogger(), defaultHeaderConfig(nil))
	if updated, err := mgr.ensureValidToken(context.Background(), &sdk.Account{Type: "api_key", Credentials: map[string]string{}}); err != nil || updated != nil {
		t.Fatalf("api key ensure token updated=%+v err=%v", updated, err)
	}
	future := &sdk.Account{Type: "oauth", Credentials: map[string]string{"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}}
	if updated, err := mgr.ensureValidToken(context.Background(), future); err != nil || updated != nil {
		t.Fatalf("future token ensure updated=%+v err=%v", updated, err)
	}

	longRefresh := strings.Repeat("r", 120)
	mgr.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusOK, `{"accessToken":"new","refreshToken":"rotated","profileArn":"arn","expiresIn":60}`), nil
	})}
	account := &sdk.Account{ID: 100, Type: "oauth", Credentials: map[string]string{"access_token": "old", "refresh_token": longRefresh}}
	updated, err := mgr.forceRefresh(context.Background(), account)
	if err != nil || updated["access_token"] != "new" || account.Credentials["refresh_token"] != "rotated" {
		t.Fatalf("social force refresh updated=%+v account=%+v err=%v", updated, account, err)
	}

	mgr.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusOK, `{"accessToken":"default-life"}`), nil
	})}
	defaultLife := &sdk.Account{ID: 105, Type: "oauth", Credentials: map[string]string{"access_token": "old", "refresh_token": longRefresh}}
	updated, err = mgr.forceRefresh(context.Background(), defaultLife)
	if err != nil || updated["expires_at"] == "" || updated["access_token"] != "default-life" {
		t.Fatalf("default lifetime refresh updated=%+v err=%v", updated, err)
	}

	var retryCalls int
	retryMgr := newTokenManager(discardLogger(), defaultHeaderConfig(nil))
	retryMgr.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		retryCalls++
		if retryCalls == 1 {
			return httpResp(http.StatusOK, `{}`), nil
		}
		return httpResp(http.StatusOK, `{"accessToken":"after-retry","expiresIn":60}`), nil
	})}
	updated, err = retryMgr.forceRefresh(context.Background(), &sdk.Account{ID: 106, Type: "oauth", Credentials: map[string]string{"access_token": "old", "refresh_token": longRefresh}})
	if err != nil || updated["access_token"] != "after-retry" || retryCalls != 2 {
		t.Fatalf("retry refresh updated=%+v err=%v calls=%d", updated, err, retryCalls)
	}

	mgr.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "oidc.us-east-1.amazonaws.com" {
			t.Fatalf("idc host = %q", req.URL.Host)
		}
		return httpResp(http.StatusOK, `{"accessToken":"idc","refreshToken":"idc-refresh","expiresIn":60}`), nil
	})}
	idc := &sdk.Account{ID: 101, Type: "oauth", Credentials: map[string]string{"access_token": "old", "refresh_token": longRefresh, "client_id": "client", "client_secret": "secret"}}
	updated, err = mgr.forceRefresh(context.Background(), idc)
	if err != nil || updated["access_token"] != "idc" {
		t.Fatalf("idc force refresh updated=%+v err=%v", updated, err)
	}

	reuseMgr := newTokenManager(discardLogger(), defaultHeaderConfig(nil))
	reuseMgr.locks.Store(int64(102), &accountRefreshState{
		lastToken:   "newer",
		latestCreds: map[string]string{"access_token": "newer", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
	})
	reuseAccount := &sdk.Account{ID: 102, Type: "oauth", Credentials: map[string]string{"access_token": "older", "refresh_token": longRefresh}}
	updated, err = reuseMgr.forceRefresh(context.Background(), reuseAccount)
	if err != nil || updated["access_token"] != "newer" || reuseAccount.Credentials["access_token"] != "newer" {
		t.Fatalf("reuse refresh updated=%+v account=%+v err=%v", updated, reuseAccount, err)
	}

	cooldownMgr := newTokenManager(discardLogger(), defaultHeaderConfig(nil))
	cooldownMgr.locks.Store(int64(103), &accountRefreshState{
		lastToken:   "same",
		lastError:   errors.New("last"),
		lastErrorAt: time.Now(),
	})
	_, err = cooldownMgr.forceRefresh(context.Background(), &sdk.Account{ID: 103, Type: "oauth", Credentials: map[string]string{"access_token": "same", "refresh_token": longRefresh}})
	if !errors.Is(err, ErrAccountDead) {
		t.Fatalf("cooldown err = %v", err)
	}

	deadMgr := newTokenManager(discardLogger(), defaultHeaderConfig(nil))
	deadMgr.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusBadRequest, "invalid_client"), nil
	})}
	_, err = deadMgr.forceRefresh(context.Background(), &sdk.Account{ID: 104, Type: "oauth", Credentials: map[string]string{"access_token": "old", "refresh_token": longRefresh}})
	if !errors.Is(err, ErrAccountDead) {
		t.Fatalf("non-retryable err = %v", err)
	}
}

func TestRefreshHTTPErrorBranches(t *testing.T) {
	mgr := newTokenManager(discardLogger(), defaultHeaderConfig(nil))
	account := &sdk.Account{Type: "oauth", Credentials: map[string]string{"refresh_token": strings.Repeat("r", 120)}}
	mgr.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("dial")
	})}
	if _, err := mgr.refreshSocial(context.Background(), account); err == nil {
		t.Fatal("social transport error should fail")
	}
	mgr.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusOK, `{}`), nil
	})}
	if _, err := mgr.refreshSocial(context.Background(), account); err == nil {
		t.Fatal("social empty response should fail")
	}
	mgr.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusInternalServerError, "boom"), nil
	})}
	if _, err := mgr.refreshSocial(context.Background(), account); err == nil {
		t.Fatal("social HTTP error should fail")
	}

	idcAccount := &sdk.Account{Type: "oauth", Credentials: map[string]string{"refresh_token": strings.Repeat("r", 120), "client_id": "client", "client_secret": "secret"}}
	mgr.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusOK, `{bad`), nil
	})}
	if _, err := mgr.refreshIdC(context.Background(), idcAccount); err == nil {
		t.Fatal("idc parse error should fail")
	}
	mgr.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusOK, `{}`), nil
	})}
	if _, err := mgr.refreshIdC(context.Background(), idcAccount); err == nil {
		t.Fatal("idc empty access token should fail")
	}
	mgr.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("dial")
	})}
	if _, err := mgr.refreshIdC(context.Background(), idcAccount); err == nil {
		t.Fatal("idc transport error should fail")
	}
	mgr.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusInternalServerError, "boom"), nil
	})}
	if _, err := mgr.refreshIdC(context.Background(), idcAccount); err == nil {
		t.Fatal("idc HTTP error should fail")
	}
}

func jwtWithClaims(claims map[string]any) string {
	payload, _ := json.Marshal(claims)
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func storeWithDeviceSession(sessionID string) *oauthSessionStore {
	store := newOAuthSessionStore()
	store.put(sessionID, &OAuthSession{
		CreatedAt:    time.Now(),
		DeviceCode:   "device",
		ClientID:     "client",
		ClientSecret: "secret",
		IDCRegion:    "us-east-1",
	})
	return store
}
