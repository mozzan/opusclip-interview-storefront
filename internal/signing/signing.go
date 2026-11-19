// Package signing implements the webhook signature scheme from SPEC section 5:
// header "t=<unix>,v1=<hex HMAC_SHA256(secret, "<t>.<raw body>")>".
package signing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

func mac(secret string, t int64, body []byte) []byte {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(strconv.FormatInt(t, 10) + "."))
	h.Write(body)
	return h.Sum(nil)
}

// Sign returns the signature header value for body at time t.
func Sign(secret string, t int64, body []byte) string {
	return "t=" + strconv.FormatInt(t, 10) + ",v1=" + hex.EncodeToString(mac(secret, t, body))
}

// Verify checks header against the exact body bytes. A tolerance of 0 skips the timestamp check.
// It returns the signed timestamp.
func Verify(secret, header string, body []byte, now time.Time, tolerance time.Duration) (int64, error) {
	var t int64
	var sig []byte
	haveT := false
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return 0, errors.New("malformed signature header")
		}
		switch k {
		case "t":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return 0, errors.New("malformed timestamp")
			}
			t, haveT = n, true
		case "v1":
			b, err := hex.DecodeString(v)
			if err != nil || len(b) != sha256.Size {
				return 0, errors.New("malformed v1 signature")
			}
			sig = b
		}
	}
	if !haveT || sig == nil {
		return 0, errors.New("signature header needs t and v1")
	}
	if !hmac.Equal(sig, mac(secret, t, body)) {
		return 0, errors.New("signature does not match")
	}
	if tolerance > 0 {
		if d := now.Sub(time.Unix(t, 0)); d > tolerance || d < -tolerance {
			return 0, errors.New("signature timestamp outside tolerance")
		}
	}
	return t, nil
}
