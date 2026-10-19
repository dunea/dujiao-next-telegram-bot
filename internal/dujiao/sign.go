package dujiao

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

func Sign(secret, method, path string, timestamp int64, body []byte) string {
	bodySum := md5.Sum(body) // Required by the Dujiao channel protocol.
	canonical := fmt.Sprintf("%s\n%s\n%d\n%s", method, path, timestamp, hex.EncodeToString(bodySum[:]))
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}

func Verify(secret, method, path string, timestamp int64, body []byte, signature string) bool {
	want := Sign(secret, method, path, timestamp, body)
	return hmac.Equal([]byte(want), []byte(signature))
}
