package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeClock is an injectable time source for deterministic window tests.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func TestLoginLimiter_BlocksAfterMax(t *testing.T) {
	l := newLoginLimiter(10, 15*time.Minute, newFakeClock().Now)

	for i := 0; i < 9; i++ {
		l.fail("1.2.3.4")
	}
	if l.blocked("1.2.3.4") {
		t.Error("9 failures must not block (max is 10)")
	}

	l.fail("1.2.3.4")
	if !l.blocked("1.2.3.4") {
		t.Error("10 failures must block")
	}
}

func TestLoginLimiter_OtherIPUnaffected(t *testing.T) {
	l := newLoginLimiter(10, 15*time.Minute, newFakeClock().Now)

	for i := 0; i < 10; i++ {
		l.fail("1.2.3.4")
	}
	if !l.blocked("1.2.3.4") {
		t.Fatal("attacker IP must be blocked after 10 failures")
	}
	if l.blocked("5.6.7.8") {
		t.Error("an unrelated IP must not be blocked")
	}
}

func TestLoginLimiter_WindowExpiryUnblocks(t *testing.T) {
	clock := newFakeClock()
	l := newLoginLimiter(10, 15*time.Minute, clock.Now)

	for i := 0; i < 10; i++ {
		l.fail("1.2.3.4")
	}
	if !l.blocked("1.2.3.4") {
		t.Fatal("expected blocked before the window ends")
	}

	clock.now = clock.now.Add(15*time.Minute + time.Second)
	if l.blocked("1.2.3.4") {
		t.Error("must unblock once the window has ended")
	}
}

func TestLoginLimiter_ResetClears(t *testing.T) {
	l := newLoginLimiter(10, 15*time.Minute, newFakeClock().Now)

	for i := 0; i < 10; i++ {
		l.fail("1.2.3.4")
	}
	l.reset("1.2.3.4")
	if l.blocked("1.2.3.4") {
		t.Error("reset after success must clear the block")
	}

	l.fail("1.2.3.4")
	if l.blocked("1.2.3.4") {
		t.Error("one failure after reset must not block")
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		xff        string
		want       string
	}{
		{
			name:       "public remote addr wins over xff",
			remoteAddr: "203.0.113.5:1",
			xff:        "1.2.3.4",
			want:       "203.0.113.5",
		},
		{
			name:       "loopback takes rightmost xff entry",
			remoteAddr: "127.0.0.1:1",
			xff:        "9.9.9.9, 1.2.3.4",
			want:       "1.2.3.4",
		},
		{
			name:       "private remote addr takes xff",
			remoteAddr: "172.18.0.2:1",
			xff:        "1.2.3.4",
			want:       "1.2.3.4",
		},
		{
			name:       "private remote addr without xff keeps remote addr",
			remoteAddr: "10.0.0.1:1",
			xff:        "",
			want:       "10.0.0.1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			if got := clientIP(r); got != tt.want {
				t.Errorf("clientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}
