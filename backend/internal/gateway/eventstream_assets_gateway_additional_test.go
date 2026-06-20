package gateway

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseHeadersAdditionalTypesAndErrors(t *testing.T) {
	var buf bytes.Buffer
	writeHeader := func(name string, typeID byte, payload []byte) {
		buf.WriteByte(byte(len(name)))
		buf.WriteString(name)
		buf.WriteByte(typeID)
		buf.Write(payload)
	}
	writeHeader("true", 0, nil)
	writeHeader("false", 1, nil)
	writeHeader("byte", 2, []byte{1})
	writeHeader("short", 3, []byte{0, 2})
	writeHeader("int", 4, []byte{0, 0, 0, 3})
	writeHeader("long", 5, []byte{0, 0, 0, 0, 0, 0, 0, 4})
	writeHeader("bytes", 6, []byte{0, 2, 'o', 'k'})
	writeHeader("string", headerTypeString, []byte{0, 5, 'v', 'a', 'l', 'u', 'e'})
	writeHeader("time", 8, make([]byte, 8))
	writeHeader("uuid", 9, make([]byte, 16))

	headers, err := parseHeaders(buf.Bytes())
	if err != nil {
		t.Fatalf("parseHeaders returned error: %v", err)
	}
	if headers["true"] != "true" || headers["false"] != "false" || headers["string"] != "value" {
		t.Fatalf("parsed headers mismatch: %#v", headers)
	}

	errorCases := [][]byte{
		{5, 'a'},
		{1, 'a'},
		{1, 'a', 6, 0},
		{1, 'a', headerTypeString, 0},
		{1, 'a', headerTypeString, 0, 4, 'x'},
		{1, 'a', 99},
	}
	for _, data := range errorCases {
		if _, err := parseHeaders(data); err == nil {
			t.Fatalf("parseHeaders(%v) expected error", data)
		}
	}
}

func TestDecodeFrameErrorBranches(t *testing.T) {
	tooSmall := make([]byte, preludeLen)
	binary.BigEndian.PutUint32(tooSmall[0:4], minMessageLen-1)
	decoder := NewEventStreamDecoder(bytes.NewReader(tooSmall))
	if _, err := decoder.decodeFrame(); err != ErrMessageTooBig {
		t.Fatalf("too small decode err = %v", err)
	}

	badPrelude := make([]byte, preludeLen)
	binary.BigEndian.PutUint32(badPrelude[0:4], minMessageLen)
	decoder = NewEventStreamDecoder(bytes.NewReader(badPrelude))
	if _, err := decoder.decodeFrame(); err != ErrPreludeCRC {
		t.Fatalf("bad prelude CRC err = %v", err)
	}

	frame := malformedFrameWithHeaderOverflow()
	decoder = NewEventStreamDecoder(bytes.NewReader(frame))
	if _, err := decoder.decodeFrame(); err != ErrMalformedFrame {
		t.Fatalf("malformed frame err = %v", err)
	}

	decoder = NewEventStreamDecoder(bytes.NewReader(frameWithBadHeader()))
	if _, err := decoder.decodeFrame(); err == nil || !strings.Contains(err.Error(), "header parse") {
		t.Fatalf("bad header frame err = %v", err)
	}

	decoder = NewEventStreamDecoder(strings.NewReader(""))
	decoder.errCount = maxConsecErrors
	if _, err := decoder.Next(); err != ErrTooManyErrors {
		t.Fatalf("too many errors err = %v", err)
	}

	if ParseAssistantResponsePayload([]byte(`{bad`)) != "" {
		t.Fatal("bad assistant payload should parse to empty string")
	}
	if _, err := ParseToolUsePayload([]byte(`{bad`)); err == nil {
		t.Fatal("bad tool use payload should fail")
	}
	if _, err := ParseContextUsagePayload([]byte(`{bad`)); err == nil {
		t.Fatal("bad context usage payload should fail")
	}
}

func malformedFrameWithHeaderOverflow() []byte {
	totalLen := uint32(minMessageLen)
	headerLen := uint32(1)
	prelude := make([]byte, 8)
	binary.BigEndian.PutUint32(prelude[0:4], totalLen)
	binary.BigEndian.PutUint32(prelude[4:8], headerLen)
	preludeCRC := crc32.ChecksumIEEE(prelude)

	var msg bytes.Buffer
	msg.Write(prelude)
	_ = binary.Write(&msg, binary.BigEndian, preludeCRC)
	msgCRC := crc32.ChecksumIEEE(msg.Bytes())
	_ = binary.Write(&msg, binary.BigEndian, msgCRC)
	return msg.Bytes()
}

func frameWithBadHeader() []byte {
	header := []byte{1, 'x', 99}
	return buildEventStreamFrameFromRawHeader(header, nil)
}

func buildEventStreamFrameFromRawHeader(header, payload []byte) []byte {
	totalLen := uint32(preludeLen + len(header) + len(payload) + messageCRCLen)
	prelude := make([]byte, 8)
	binary.BigEndian.PutUint32(prelude[0:4], totalLen)
	binary.BigEndian.PutUint32(prelude[4:8], uint32(len(header)))
	preludeCRC := crc32.ChecksumIEEE(prelude)

	var msg bytes.Buffer
	msg.Write(prelude)
	_ = binary.Write(&msg, binary.BigEndian, preludeCRC)
	msg.Write(header)
	msg.Write(payload)
	msgCRC := crc32.ChecksumIEEE(msg.Bytes())
	_ = binary.Write(&msg, binary.BigEndian, msgCRC)
	return msg.Bytes()
}

func TestAssetsLoading(t *testing.T) {
	if got := loadAssetsFromDir(filepath.Join(t.TempDir(), "missing")); got != nil {
		t.Fatalf("missing assets dir = %#v", got)
	}
	empty := t.TempDir()
	if got := loadAssetsFromDir(empty); got != nil {
		t.Fatalf("empty assets dir = %#v", got)
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	assets := loadAssetsFromDir(root)
	if string(assets["nested/app.js"]) != "console.log(1)" {
		t.Fatalf("assets = %#v", assets)
	}

	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, "web", "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "web", "dist", "index.js"), []byte("dev"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	if devAssets := loadDevWebAssets(); string(devAssets["index.js"]) != "dev" {
		t.Fatalf("dev assets = %#v", devAssets)
	}
	if webAssets := (&KiroGateway{}).GetWebAssets(); string(webAssets["index.js"]) != "dev" {
		t.Fatalf("web assets = %#v", webAssets)
	}

	project = t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, "web", "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "web", "dist", "first.js"), []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	backendDir := filepath.Join(project, "backend")
	if err := os.MkdirAll(backendDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(backendDir)
	if devAssets := loadDevWebAssets(); string(devAssets["first.js"]) != "first" {
		t.Fatalf("first dev assets path = %#v", devAssets)
	}

	t.Chdir(t.TempDir())
	if fallback := (&KiroGateway{}).GetWebAssets(); len(fallback) == 0 || len(fallback["placeholder.txt"]) != 0 {
		t.Fatalf("embedded fallback assets = %#v", fallback)
	}
}

func TestGatewayInitStopAndUsageHandlers(t *testing.T) {
	g := &KiroGateway{}
	if err := g.Init(testPluginContext{logger: discardLogger(), config: testPluginConfig{"kiro_version": "test"}}); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	if g.logger == nil || g.ctx == nil || g.tokenMgr == nil || g.oauthStore == nil || g.client == nil || g.cancelCleanup == nil {
		t.Fatalf("gateway not initialized: %+v", g)
	}
	if err := g.Stop(context.Background()); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}

	usageBody := `{
		"nextDateReset": 1893456000,
		"usageBreakdownList": [{
			"currentUsageWithPrecision": 2,
			"usageLimitWithPrecision": 10,
			"nextDateReset": 1893456000
		}]
	}`
	g = &KiroGateway{
		logger:     discardLogger(),
		ctx:        testPluginContext{config: testPluginConfig{}},
		headerCfg:  defaultHeaderConfig(nil),
		tokenMgr:   newTokenManager(discardLogger(), defaultHeaderConfig(nil)),
		oauthStore: newOAuthSessionStore(),
		client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return httpResp(http.StatusOK, usageBody), nil
		})},
	}
	accountBody := []byte(`[{"id":7,"credentials":{"type":"api_key","kiro_api_key":"ksk"}}]`)
	status, _, body, err := g.HandleRequest(context.Background(), http.MethodPost, "usage/accounts", "", nil, accountBody)
	if err != nil || status != http.StatusOK || !strings.Contains(string(body), `"7"`) {
		t.Fatalf("usage accounts status=%d err=%v body=%s", status, err, string(body))
	}
	status, _, _, err = g.HandleRequest(context.Background(), http.MethodPost, "usage/accounts", "", nil, []byte(`bad`))
	if err != nil || status != http.StatusBadRequest {
		t.Fatalf("invalid usage accounts status=%d err=%v", status, err)
	}

	probeBody := []byte(`{"id":8,"credentials":{"type":"api_key","kiro_api_key":"ksk"}}`)
	status, _, body, err = g.HandleRequest(context.Background(), http.MethodPost, "usage/probe", "", nil, probeBody)
	if err != nil || status != http.StatusOK || !strings.Contains(string(body), `"8"`) {
		t.Fatalf("usage probe status=%d err=%v body=%s", status, err, string(body))
	}
	status, _, _, err = g.HandleRequest(context.Background(), http.MethodPost, "usage/probe", "", nil, []byte(`{}`))
	if err != nil || status != http.StatusBadRequest {
		t.Fatalf("invalid usage probe status=%d err=%v", status, err)
	}

	g.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusInternalServerError, "boom"), nil
	})}
	status, _, _, err = g.HandleRequest(context.Background(), http.MethodPost, "usage/probe", "", nil, probeBody)
	if err != nil || status != http.StatusInternalServerError {
		t.Fatalf("failed usage probe status=%d err=%v", status, err)
	}

	quota, err := g.QueryQuota(context.Background(), map[string]string{"type": "api_key", "kiro_api_key": "ksk"})
	if err == nil || quota != nil {
		t.Fatalf("QueryQuota should return error after upstream 500, quota=%+v err=%v", quota, err)
	}
}

func TestGatewayQuotaValidationAndAccountQuotaErrors(t *testing.T) {
	g := &KiroGateway{
		logger:    discardLogger(),
		ctx:       testPluginContext{config: testPluginConfig{}},
		headerCfg: defaultHeaderConfig(nil),
		tokenMgr:  newTokenManager(discardLogger(), defaultHeaderConfig(nil)),
		client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return httpResp(http.StatusOK, `{"usageBreakdownList":[{"currentUsageWithPrecision":1,"usageLimitWithPrecision":2}]}`), nil
		})},
	}
	if err := g.ValidateAccount(context.Background(), map[string]string{"type": "api_key", "kiro_api_key": "ksk"}); err != nil {
		t.Fatalf("api key validation failed: %v", err)
	}
	quota, err := g.QueryQuota(context.Background(), map[string]string{"type": "api_key", "kiro_api_key": "ksk", "profile_arn": "arn"})
	if err != nil || quota == nil || quota.Total != 2 {
		t.Fatalf("QueryQuota quota=%+v err=%v", quota, err)
	}

	status, _, _, err := g.HandleRequest(context.Background(), http.MethodPost, "accounts/quota", "", nil, []byte(`{"id":9,"credentials":{"refresh_token":"short"}}`))
	if err != nil || status != http.StatusUnauthorized {
		t.Fatalf("dead account quota status=%d err=%v", status, err)
	}
	g.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusInternalServerError, "boom"), nil
	})}
	status, _, _, err = g.HandleRequest(context.Background(), http.MethodPost, "accounts/quota", "", nil, []byte(`{"id":10,"credentials":{"type":"api_key","kiro_api_key":"ksk"}}`))
	if err != nil || status != http.StatusInternalServerError {
		t.Fatalf("upstream error account quota status=%d err=%v", status, err)
	}
	status, _, _, err = g.HandleRequest(context.Background(), http.MethodPost, "missing", "", nil, nil)
	if err != nil || status != http.StatusNotFound {
		t.Fatalf("missing route status=%d err=%v", status, err)
	}

	if windows := g.buildUsageWindows(nil, nil, time.Now()); windows != nil {
		t.Fatalf("nil quota windows = %#v", windows)
	}
	if windows := g.buildUsageWindows(&quotaInfo{Total: 0}, nil, time.Now()); windows != nil {
		t.Fatalf("zero quota windows = %#v", windows)
	}
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	windows := g.buildUsageWindows(&quotaInfo{Used: 1, Total: 4, ExpiresAt: past}, map[string]string{"ignore_usage_limit": "on"}, time.Now())
	if len(windows) != 1 || windows[0].ResetSeconds != 0 || !windows[0].IgnoreLimit {
		t.Fatalf("past reset windows = %#v", windows)
	}
	if normalizePlanName("") != "" || formatUsageNumber(1.25) != "1.25" {
		t.Fatal("plan or usage formatting mismatch")
	}

	outcome := g.probeUsage(context.Background(), 11, map[string]string{"type": "oauth", "refresh_token": "short"})
	if outcome != nil {
		t.Fatalf("oauth probe with invalid refresh token should fail: %+v", outcome)
	}

	g.usageCache.Store(int64(12), &usageCacheEntry{quota: &quotaInfo{Used: 1, Total: 2}, capturedAt: time.Now()})
	if cached := g.getUsageCached(context.Background(), 12, nil); cached == nil || cached.Total != 2 {
		t.Fatalf("cached usage = %+v", cached)
	}

	var usageCalls int
	g.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		usageCalls++
		if usageCalls == 1 {
			return httpResp(http.StatusForbidden, "bearer token is invalid"), nil
		}
		return httpResp(http.StatusOK, `{"usageBreakdownList":[{"currentUsageWithPrecision":3,"usageLimitWithPrecision":9}]}`), nil
	})}
	g.tokenMgr.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusOK, `{"accessToken":"fresh","refreshToken":"fresh-refresh","expiresIn":60}`), nil
	})}
	quota, err = g.queryQuota(context.Background(), 13, map[string]string{
		"access_token":  "stale",
		"refresh_token": strings.Repeat("r", 120),
		"expires_at":    time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
	if err != nil || quota == nil || quota.Total != 9 || usageCalls != 2 {
		t.Fatalf("queryQuota refresh quota=%+v err=%v calls=%d", quota, err, usageCalls)
	}

	usageCalls = 0
	probed := g.probeUsage(context.Background(), 14, map[string]string{
		"access_token":  "stale",
		"refresh_token": strings.Repeat("r", 120),
		"expires_at":    time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
	if probed == nil || probed.Total != 9 || usageCalls != 2 {
		t.Fatalf("probe refresh quota=%+v calls=%d", probed, usageCalls)
	}

	if err := g.ValidateAccount(context.Background(), map[string]string{
		"access_token":  "fresh",
		"refresh_token": strings.Repeat("r", 120),
		"expires_at":    time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("oauth ValidateAccount failed: %v", err)
	}
}

func TestHandleRequestOAuthSimpleBranches(t *testing.T) {
	g := &KiroGateway{
		logger:     discardLogger(),
		oauthStore: newOAuthSessionStore(),
		callbackLn: &callbackListener{logger: discardLogger(), running: true},
		client:     &http.Client{},
	}
	status, _, body, err := g.HandleRequest(context.Background(), http.MethodPost, "oauth/start", "", nil, nil)
	if err != nil || status != http.StatusOK || !strings.Contains(string(body), "authorize_url") || !strings.Contains(string(body), "auto_callback") {
		t.Fatalf("oauth start status=%d err=%v body=%s", status, err, string(body))
	}
	status, _, _, err = g.HandleRequest(context.Background(), http.MethodPost, "oauth/exchange", "", nil, []byte(`{}`))
	if err != nil || status != http.StatusBadRequest {
		t.Fatalf("oauth exchange invalid status=%d err=%v", status, err)
	}
	status, _, _, err = g.HandleRequest(context.Background(), http.MethodPost, "oauth/status", "", nil, []byte(`{}`))
	if err != nil || status != http.StatusBadRequest {
		t.Fatalf("oauth status invalid status=%d err=%v", status, err)
	}
	status, _, _, err = g.HandleRequest(context.Background(), http.MethodPost, "oauth/device-complete", "", nil, []byte(`{}`))
	if err != nil || status != http.StatusBadRequest {
		t.Fatalf("device complete invalid status=%d err=%v", status, err)
	}

	status, _, body, err = g.HandleRequest(context.Background(), http.MethodPost, "oauth/status", "", nil, []byte(`{"session_id":"missing"}`))
	if err != nil || status != http.StatusBadRequest || !strings.Contains(string(body), "session expired") {
		t.Fatalf("oauth status missing status=%d err=%v body=%s", status, err, string(body))
	}
	g.callbackLn.running = false
	status, _, body, err = g.HandleRequest(context.Background(), http.MethodPost, "oauth/status", "", nil, []byte(`{"session_id":"missing"}`))
	if err != nil || status != http.StatusOK || !strings.Contains(string(body), "unavailable") {
		t.Fatalf("oauth status unavailable status=%d err=%v body=%s", status, err, string(body))
	}

	status, _, body, err = g.HandleRequest(context.Background(), http.MethodPost, "oauth/exchange", "", nil, []byte(`{"callback_url":"poll:missing"}`))
	if err != nil || status != http.StatusOK || !strings.Contains(string(body), "unavailable") {
		t.Fatalf("poll exchange status=%d err=%v body=%s", status, err, string(body))
	}
	status, _, body, err = g.HandleRequest(context.Background(), http.MethodPost, "oauth/exchange", "", nil, []byte(`{"callback_url":"device-complete:missing"}`))
	if err != nil || status != http.StatusBadRequest || !strings.Contains(string(body), "设备授权会话") {
		t.Fatalf("device exchange status=%d err=%v body=%s", status, err, string(body))
	}
}

func TestHandleRequestOAuthCompletionBranches(t *testing.T) {
	tokenBody := `{"accessToken":"` + jwtWithClaims(map[string]any{"email": "oauth@example.com"}) + `","refreshToken":"refresh","expiresIn":60}`
	g := &KiroGateway{
		logger:     discardLogger(),
		oauthStore: newOAuthSessionStore(),
		callbackLn: &callbackListener{logger: discardLogger(), running: true},
		client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return httpResp(http.StatusOK, tokenBody), nil
		})},
	}
	g.oauthStore.put("sess", &OAuthSession{State: "state", CodeVerifier: "verifier", CreatedAt: time.Now()})
	status, _, body, err := g.HandleRequest(context.Background(), http.MethodPost, "oauth/exchange", "", nil, []byte(`{"callback_url":"?state=state&code=code"}`))
	if err != nil || status != http.StatusOK || !strings.Contains(string(body), "oauth@example.com") {
		t.Fatalf("oauth exchange completion status=%d err=%v body=%s", status, err, string(body))
	}

	g.oauthStore.put("poll", &OAuthSession{State: "poll-state", CodeVerifier: "verifier", CreatedAt: time.Now()})
	status, _, body, err = g.HandleRequest(context.Background(), http.MethodPost, "oauth/status", "", nil, []byte(`{"session_id":"poll"}`))
	if err != nil || status != http.StatusOK || !strings.Contains(string(body), "pending") {
		t.Fatalf("oauth poll pending status=%d err=%v body=%s", status, err, string(body))
	}
	g.callbackLn.captured.Store("poll-state", kiroCallbackBaseURL+"/oauth/callback?state=poll-state&code=code")
	status, _, body, err = g.HandleRequest(context.Background(), http.MethodPost, "oauth/status", "", nil, []byte(`{"session_id":"poll"}`))
	if err != nil || status != http.StatusOK || !strings.Contains(string(body), "complete") {
		t.Fatalf("oauth poll complete status=%d err=%v body=%s", status, err, string(body))
	}

	g.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusBadRequest, `{"error":"authorization_pending"}`), nil
	})}
	g.oauthStore.put("device", &OAuthSession{CreatedAt: time.Now(), DeviceCode: "device", ClientID: "client", ClientSecret: "secret", IDCRegion: "us-east-1"})
	status, _, body, err = g.HandleRequest(context.Background(), http.MethodPost, "oauth/device-complete", "", nil, []byte(`{"session_id":"device"}`))
	if err != nil || status != http.StatusOK || !strings.Contains(string(body), "pending") {
		t.Fatalf("device pending status=%d err=%v body=%s", status, err, string(body))
	}

	g.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusOK, `{"accessToken":"`+jwtWithClaims(map[string]any{"email": "device@example.com"})+`","refreshToken":"refresh","expiresIn":60}`), nil
	})}
	g.oauthStore.put("device-done", &OAuthSession{CreatedAt: time.Now(), DeviceCode: "device", ClientID: "client", ClientSecret: "secret", IDCRegion: "us-east-1"})
	status, _, body, err = g.HandleRequest(context.Background(), http.MethodPost, "oauth/device-complete", "", nil, []byte(`{"session_id":"device-done"}`))
	if err != nil || status != http.StatusOK || !strings.Contains(string(body), "device@example.com") {
		t.Fatalf("device complete status=%d err=%v body=%s", status, err, string(body))
	}

	g.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/client/register":
			return httpResp(http.StatusOK, `{"clientId":"client","clientSecret":"secret"}`), nil
		case "/device_authorization":
			return httpResp(http.StatusOK, `{"deviceCode":"device","userCode":"user","verificationUri":"https://verify"}`), nil
		default:
			return httpResp(http.StatusOK, tokenBody), nil
		}
	})}
	g.oauthStore.put("builder", &OAuthSession{State: "builder-state", CodeVerifier: "verifier", CreatedAt: time.Now()})
	status, _, body, err = g.HandleRequest(context.Background(), http.MethodPost, "oauth/exchange", "", nil, []byte(`{"callback_url":"?state=builder-state&login_option=builderid&issuer_url=https://issuer"}`))
	if err != nil || status != http.StatusOK || !strings.Contains(string(body), "__device_auth__") {
		t.Fatalf("builder exchange status=%d err=%v body=%s", status, err, string(body))
	}

	g.oauthStore.put("poll-builder", &OAuthSession{State: "poll-builder-state", CodeVerifier: "verifier", CreatedAt: time.Now()})
	g.callbackLn.captured.Store("poll-builder-state", kiroCallbackBaseURL+"/oauth/callback?state=poll-builder-state&login_option=builderid&issuer_url=https://issuer")
	status, _, body, err = g.HandleRequest(context.Background(), http.MethodPost, "oauth/status", "", nil, []byte(`{"session_id":"poll-builder"}`))
	if err != nil || status != http.StatusOK || !strings.Contains(string(body), "device_auth") {
		t.Fatalf("poll builder status=%d err=%v body=%s", status, err, string(body))
	}

	g.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpResp(http.StatusBadRequest, "bad"), nil
	})}
	g.oauthStore.put("bad-exchange", &OAuthSession{State: "bad-state", CodeVerifier: "verifier", CreatedAt: time.Now()})
	status, _, body, err = g.HandleRequest(context.Background(), http.MethodPost, "oauth/exchange", "", nil, []byte(`{"callback_url":"?state=bad-state&code=code"}`))
	if err != nil || status != http.StatusBadRequest {
		t.Fatalf("bad exchange status=%d err=%v body=%s", status, err, string(body))
	}
	g.oauthStore.put("bad-poll", &OAuthSession{State: "bad-poll-state", CodeVerifier: "verifier", CreatedAt: time.Now()})
	g.callbackLn.captured.Store("bad-poll-state", kiroCallbackBaseURL+"/oauth/callback?state=bad-poll-state&code=code")
	status, _, body, err = g.HandleRequest(context.Background(), http.MethodPost, "oauth/status", "", nil, []byte(`{"session_id":"bad-poll"}`))
	if err != nil || status != http.StatusBadRequest {
		t.Fatalf("bad poll status=%d err=%v body=%s", status, err, string(body))
	}
	g.oauthStore.put("device-error", &OAuthSession{CreatedAt: time.Now(), DeviceCode: "device", ClientID: "client", ClientSecret: "secret", IDCRegion: "us-east-1"})
	status, _, body, err = g.HandleRequest(context.Background(), http.MethodPost, "oauth/device-complete", "", nil, []byte(`{"session_id":"device-error"}`))
	if err != nil || status != http.StatusBadRequest || !strings.Contains(string(body), "device token HTTP") {
		t.Fatalf("device error status=%d err=%v body=%s", status, err, string(body))
	}
}

func TestCallbackListenerStartAlreadyRunningWhenAvailable(t *testing.T) {
	listener := newCallbackListener(discardLogger())
	if !listener.start() {
		t.Skip("callback listener port unavailable")
	}
	defer listener.stop()
	if !listener.start() {
		t.Fatal("already running listener should return true")
	}
}
