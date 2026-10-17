package handler

import (
	"testing"
	"time"

	// Embed the tz database so Europe/Moscow resolves in any build
	// environment (CI containers may ship without system tzdata).
	_ "time/tzdata"
)

// TestValidators covers the pure boundary validators used by every handler.
func TestValidators(t *testing.T) {
	cases := []struct {
		name string
		got  bool
		want bool
	}{
		{"isDate 2026-02-29 (not a leap year)", isDate("2026-02-29"), false},
		{"isDate 2028-02-29 (leap year)", isDate("2028-02-29"), true},
		{"isDate 2026-13-01", isDate("2026-13-01"), false},
		{"isDate 2026-02-31 (normalizes, must reject)", isDate("2026-02-31"), false},
		{"isDate 2026-01-01", isDate("2026-01-01"), true},
		{"isDate 2026-1-1 (not zero-padded)", isDate("2026-1-1"), false},
		{"isWhenDate someday", isWhenDate("someday"), true},
		{"isWhenDate 2026-06-01", isWhenDate("2026-06-01"), true},
		{"isWhenDate tomorrow", isWhenDate("tomorrow"), false},
		{"isClock 9:00", isClock("9:00"), false},
		{"isClock 09:00", isClock("09:00"), true},
		{"isClock 24:00", isClock("24:00"), false},
		{"isClock 09:60", isClock("09:60"), false},
		{"isClock 23:59", isClock("23:59"), true},
		{"isClientID 'a b' (space)", isClientID("a b"), false},
		{"isClientID 'abc' (too short)", isClientID("abc"), false},
		{"isClientID 'Ab1_-zZ89'", isClientID("Ab1_-zZ89"), true},
		{"isClientID 22 chars (too long)", isClientID("1234567890123456789012"), false},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: expected %v, got %v", tc.name, tc.want, tc.got)
		}
	}
}

// TestNormalizeExactAt covers the exact-reminder time normalizer: RFC 3339
// with a numeric offset or Z passes through as UTC, a naive timestamp is read
// in the given location, and garbage is rejected.
func TestNormalizeExactAt(t *testing.T) {
	msk, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatalf("load Europe/Moscow: %v", err)
	}
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"offset +03:00", "2026-10-20T09:00:00+03:00", "2026-10-20T06:00:00Z"},
		{"Z", "2026-10-20T06:00:00Z", "2026-10-20T06:00:00Z"},
		{"negative offset", "2026-10-20T01:30:00-05:00", "2026-10-20T06:30:00Z"},
		{"naive without seconds in Europe/Moscow", "2026-10-20T09:00", "2026-10-20T06:00:00Z"},
		{"naive with seconds in Europe/Moscow", "2026-10-20T09:00:30", "2026-10-20T06:00:30Z"},
	}
	for _, tc := range cases {
		got, err := normalizeExactAt(tc.in, msk)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: normalizeExactAt(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{
		"", "not-a-time", "2026-10-20", "2026-10-20 09:00",
		"2026-13-01T09:00", "2026-02-31T09:00", "2026-10-20T25:00",
	} {
		if got, err := normalizeExactAt(bad, msk); err == nil {
			t.Errorf("normalizeExactAt(%q): expected error, got %q", bad, got)
		}
	}
}
