package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func peerRequest(p *peer, path string, values url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	return w
}
func TestPeerRealSDKEmptyAndMultipartCompatibleMethods(t *testing.T) {
	p := &peer{secret: strings.Repeat("a", 32)}
	for _, method := range []string{"getMe", "setMyCommands", "getUpdates", "answerCallbackQuery"} {
		w := peerRequest(p, "/bot"+testToken+"/"+method, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
			t.Fatalf("%s returned %d", method, w.Code)
		}
	}
	if peerRequest(p, "/bot"+testToken+"/unsupported", nil).Code != 404 {
		t.Fatal("unsupported method silently accepted")
	}
	if peerRequest(p, "/bot"+testToken+"/sendMessage", url.Values{"chat_id": {"42"}, "text": {"never send to real users"}}).Code != 400 {
		t.Fatal("non-synthetic recipient accepted")
	}
}
func TestPeerGatewayRequiresIndependentSignature(t *testing.T) {
	p := &peer{secret: strings.Repeat("a", 32)}
	form := url.Values{"pid": {"isolated"}, "out_trade_no": {"SYNTHETIC1"}, "money": {"103.50"}, "sign_type": {"MD5"}}
	form.Set("sign", "bad")
	if peerRequest(p, "/mapi.php", form).Code != 400 || len(p.payments) != 0 {
		t.Fatal("unsigned gateway created payment")
	}
	form.Set("sign", sign(form, p.secret))
	w := peerRequest(p, "/mapi.php", form)
	if w.Code != 200 || len(p.payments) != 1 || p.payments[0].Amount != "103.50" {
		t.Fatal("gateway fixture request did not retain amount")
	}
	var result map[string]any
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || result["code"] != float64(1) {
		t.Fatal("invalid Epay response")
	}
}
func TestPeerTemporaryFaultDoesNotAcknowledgeMessage(t *testing.T) {
	p := &peer{fault: fault{Method: "sendMessage", Code: 429, Times: 1, RetryAfter: 2}}
	values := url.Values{"chat_id": {"910000001"}, "text": {"synthetic"}}
	if peerRequest(p, "/bot"+testToken+"/sendMessage", values).Code != 429 || len(p.messages) != 0 {
		t.Fatal("failed send recorded as accepted")
	}
	if peerRequest(p, "/bot"+testToken+"/sendMessage", values).Code != 200 || len(p.messages) != 1 {
		t.Fatal("fault did not clear")
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}
