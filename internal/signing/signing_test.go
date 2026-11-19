package signing

import (
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	now := time.Unix(1763550000, 0)
	body := []byte(`{"id":"evt_1"}`)
	h := Sign("whsec", now.Unix(), body)
	if _, err := Verify("whsec", h, body, now, 300*time.Second); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	cases := map[string]func() error{
		"wrong secret": func() error { _, e := Verify("other", h, body, now, 300*time.Second); return e },
		"tampered":     func() error { _, e := Verify("whsec", h, []byte(`{"id":"evt_2"}`), now, 300*time.Second); return e },
		"stale":        func() error { _, e := Verify("whsec", h, body, now.Add(301*time.Second), 300*time.Second); return e },
		"garbage":      func() error { _, e := Verify("whsec", "garbage", body, now, 300*time.Second); return e },
		"no v1":        func() error { _, e := Verify("whsec", "t=123", body, now, 300*time.Second); return e },
	}
	for name, f := range cases {
		if f() == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := Verify("whsec", h, body, now.Add(10*time.Hour), 0); err != nil {
		t.Errorf("tolerance 0 should skip the time check: %v", err)
	}
}
