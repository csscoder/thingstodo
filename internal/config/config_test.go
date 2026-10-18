package config

import (
	"errors"
	"testing"
	"time"
)

func TestZoneName(t *testing.T) {
	moscow, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatalf("load Europe/Moscow: %v", err)
	}

	tests := []struct {
		name     string
		loc      *time.Location
		target   func() (string, error)
		expected string
	}{
		{
			name:     "named location passes through",
			loc:      moscow,
			target:   func() (string, error) { return "", errors.New("must not be called") },
			expected: "Europe/Moscow",
		},
		{
			name:     "local resolves via /etc/localtime target",
			loc:      time.Local,
			target:   func() (string, error) { return "/usr/share/zoneinfo/Asia/Tokyo", nil },
			expected: "Asia/Tokyo",
		},
		{
			name:     "local with unreadable symlink falls back to UTC",
			loc:      time.Local,
			target:   func() (string, error) { return "", errors.New("no symlink") },
			expected: "UTC",
		},
		{
			name:     "local with target lacking zoneinfo segment falls back to UTC",
			loc:      time.Local,
			target:   func() (string, error) { return "/etc/localtime", nil },
			expected: "UTC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := zoneName(tt.loc, tt.target); got != tt.expected {
				t.Errorf("zoneName(%v) = %q, want %q", tt.loc, got, tt.expected)
			}
		})
	}
}

// TestLoadUsesZoneItReports covers the Load wiring for the TZ-unset path:
// when the name resolved from /etc/localtime is a loadable zone, Load must
// point cfg.Location at it so the server computes dates in the zone it
// reports. time.Local is a process-global singleton (and TZ="" would
// initialize it to UTC), so the test fakes it with a "Local"-named zone.
func TestLoadUsesZoneItReports(t *testing.T) {
	origLocal := time.Local
	defer func() { time.Local = origLocal }()
	time.Local = time.FixedZone("Local", 5*60*60)

	t.Setenv("TZ", "")
	t.Setenv("JWT_SECRET", "test-secret")

	cfg := Load()
	if cfg.TimeZoneName == "UTC" {
		t.Skip("host has no resolvable /etc/localtime zone")
	}
	if _, err := time.LoadLocation(cfg.TimeZoneName); err != nil {
		t.Skipf("resolved name %q is not loadable on this host", cfg.TimeZoneName)
	}
	if cfg.Location.String() != cfg.TimeZoneName {
		t.Errorf("Load with TZ unset: Location %q, want the reported zone %q", cfg.Location, cfg.TimeZoneName)
	}
}

func TestValidate_BuiltinRequiresJWTSecret(t *testing.T) {
	if err := (Config{AuthMode: "builtin"}).Validate(); err == nil {
		t.Error("expected error for builtin mode without JWTSecret, got nil")
	}
	if err := (Config{AuthMode: "builtin", JWTSecret: "s"}).Validate(); err != nil {
		t.Errorf("expected no error for builtin mode with JWTSecret, got %v", err)
	}
}

func TestValidate_OIDCRequiresAllowedEmail(t *testing.T) {
	if err := (Config{AuthMode: "oidc", JWTSecret: "s"}).Validate(); err == nil {
		t.Error("expected error for oidc mode without OIDCAllowedEmail, got nil")
	}
	if err := (Config{AuthMode: "oidc", JWTSecret: "s", OIDCAllowedEmail: "user@example.com"}).Validate(); err != nil {
		t.Errorf("expected no error for oidc mode with OIDCAllowedEmail, got %v", err)
	}
}
