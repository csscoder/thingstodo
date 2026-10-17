package handler

import "testing"

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
