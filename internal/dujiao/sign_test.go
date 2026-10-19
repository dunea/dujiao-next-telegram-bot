package dujiao

import "testing"

func TestSignAndVerify(t *testing.T) {
	body := []byte(`{"channel_user_id":"123"}`)
	signature := Sign("secret", "POST", "/api/v1/channel/identities/telegram/resolve", 1700000000, body)
	if signature == "" {
		t.Fatal("signature is empty")
	}
	if !Verify("secret", "POST", "/api/v1/channel/identities/telegram/resolve", 1700000000, body, signature) {
		t.Fatal("signature did not verify")
	}
	if Verify("wrong", "POST", "/api/v1/channel/identities/telegram/resolve", 1700000000, body, signature) {
		t.Fatal("signature unexpectedly verified with wrong secret")
	}
}
