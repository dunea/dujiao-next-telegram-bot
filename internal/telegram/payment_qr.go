package telegram

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	qrcode "github.com/yeqown/go-qrcode/v2"
	"github.com/yeqown/go-qrcode/writer/standard"
)

const maxQRCodeImageBytes = 8 << 20

var directQRImagePattern = regexp.MustCompile(`(?i)^https?://[^\s]+\.(png|jpe?g|gif|webp)(\?[^\s]*)?$`)

type paymentQRImage struct {
	URL  string
	Data []byte
}

func buildPaymentQRImage(qrCode, payURL, interactionMode string) (paymentQRImage, bool, error) {
	if !strings.EqualFold(strings.TrimSpace(interactionMode), "qr") {
		return paymentQRImage{}, false, nil
	}
	content := strings.TrimSpace(qrCode)
	if content == "" {
		content = strings.TrimSpace(payURL)
	}
	if content == "" {
		return paymentQRImage{}, false, nil
	}
	if directQRImagePattern.MatchString(content) && safeHTTPURL(content) {
		return paymentQRImage{URL: content}, true, nil
	}
	if strings.HasPrefix(strings.ToLower(content), "data:image/") {
		data, err := decodeQRDataURI(content)
		if err != nil {
			return paymentQRImage{}, false, err
		}
		return paymentQRImage{Data: data}, true, nil
	}
	data, err := renderQRCodePNG(content)
	if err != nil {
		return paymentQRImage{}, false, err
	}
	return paymentQRImage{Data: data}, true, nil
}

func decodeQRDataURI(value string) ([]byte, error) {
	header, encoded, found := strings.Cut(value, ",")
	if !found || !strings.Contains(strings.ToLower(header), ";base64") {
		return nil, errors.New("QR image data URI must be base64 encoded")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode QR image: %w", err)
	}
	if len(data) == 0 || len(data) > maxQRCodeImageBytes {
		return nil, errors.New("QR image size is invalid")
	}
	if contentType := http.DetectContentType(data); !strings.HasPrefix(contentType, "image/") {
		return nil, errors.New("QR data URI is not an image")
	}
	return data, nil
}

func renderQRCodePNG(content string) ([]byte, error) {
	if len([]byte(content)) > 4096 {
		return nil, errors.New("QR content is too large")
	}
	code, err := qrcode.NewWith(content, qrcode.WithErrorCorrectionLevel(qrcode.ErrorCorrectionMedium))
	if err != nil {
		return nil, fmt.Errorf("create QR code: %w", err)
	}
	buffer := &qrBuffer{}
	writer := standard.NewWithWriter(buffer,
		standard.WithBuiltinImageEncoder(standard.PNG_FORMAT),
		standard.WithQRWidth(10),
	)
	if err := code.Save(writer); err != nil {
		return nil, fmt.Errorf("render QR code: %w", err)
	}
	return append([]byte(nil), buffer.Bytes()...), nil
}

type qrBuffer struct {
	bytes.Buffer
}

func (*qrBuffer) Close() error { return nil }
