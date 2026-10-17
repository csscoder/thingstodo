package repository

import (
	"testing"
	"time"
)

func tzLoadLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return loc
}

func TestLocalDayUTCBounds_FixedOffsets(t *testing.T) {
	// Moscow is UTC+3 year-round: local midnight is 21:00 UTC the day before.
	start, end := localDayUTCBounds("2026-10-17", tzLoadLocation(t, "Europe/Moscow"))
	if start != "2026-10-16 21:00:00" || end != "2026-10-17 21:00:00" {
		t.Errorf("Moscow bounds for 2026-10-17: got %q..%q", start, end)
	}

	start, end = localDayUTCBounds("2026-10-17", time.UTC)
	if start != "2026-10-17 00:00:00" || end != "2026-10-18 00:00:00" {
		t.Errorf("UTC bounds for 2026-10-17: got %q..%q", start, end)
	}

	// Kathmandu, UTC+5:45: bounds land on a 45-minute offset.
	start, end = localDayUTCBounds("2026-10-17", tzLoadLocation(t, "Asia/Kathmandu"))
	if start != "2026-10-16 18:15:00" || end != "2026-10-17 18:15:00" {
		t.Errorf("Kathmandu bounds for 2026-10-17: got %q..%q", start, end)
	}
}

func TestLocalDayUTCBounds_DSTTransitions(t *testing.T) {
	ny := tzLoadLocation(t, "America/New_York")

	// 2026-03-08: US spring forward; the local day lasts 23 hours.
	start, end := localDayUTCBounds("2026-03-08", ny)
	if start != "2026-03-08 05:00:00" || end != "2026-03-09 04:00:00" {
		t.Errorf("spring-forward bounds for 2026-03-08: got %q..%q", start, end)
	}

	// 2026-11-01: US fall back; the local day lasts 25 hours.
	start, end = localDayUTCBounds("2026-11-01", ny)
	if start != "2026-11-01 04:00:00" || end != "2026-11-02 05:00:00" {
		t.Errorf("fall-back bounds for 2026-11-01: got %q..%q", start, end)
	}
}
