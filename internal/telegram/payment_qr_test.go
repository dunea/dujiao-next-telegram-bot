package telegram

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestBuildPaymentQRImageRendersContent(t *testing.T) {
	image, ok, err := buildPaymentQRImage("weixin://wxpay/example", "", "qr")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || len(image.Data) < 8 || string(image.Data[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("unexpected PNG: ok=%v bytes=%d", ok, len(image.Data))
	}
}

func TestBuildPaymentQRImageUsesDirectImageURL(t *testing.T) {
	image, ok, err := buildPaymentQRImage("https://pay.example.test/qr.png?id=1", "", "qr")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || image.URL == "" || len(image.Data) != 0 {
		t.Fatalf("image = %#v, ok = %v", image, ok)
	}
}

func TestBuildPaymentQRImageDecodesDataURI(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nmock")
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	image, ok, err := buildPaymentQRImage(uri, "", "qr")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || string(image.Data) != string(png) {
		t.Fatalf("image = %#v, ok = %v", image, ok)
	}
}

func TestBuildPaymentQRImageSkipsRedirect(t *testing.T) {
	_, ok, err := buildPaymentQRImage("content", "", "redirect")
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestRenderQRCodePNGRejectsOversizedContent(t *testing.T) {
	_, err := renderQRCodePNG(strings.Repeat("x", 4097))
	if err == nil {
		t.Fatal("expected oversized QR content error")
	}
}
