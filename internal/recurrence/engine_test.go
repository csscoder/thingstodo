package recurrence

import (
	"testing"
	"time"

	"github.com/collinjanssen/thingstodo/internal/model"
)

func intPtr(n int) *int { return &n }

func TestEngine(t *testing.T) {
	engine := NewEngine()

	tests := []struct {
		name     string
		from     string
		pattern  model.RecurrencePattern
		expected string
	}{
		// --- Daily ---
		{
			name:     "daily every 1 day",
			from:     "2025-03-10",
			pattern:  model.RecurrencePattern{Type: model.PatternDaily, Every: 1, Mode: "fixed"},
			expected: "2025-03-11",
		},
		{
			name:     "daily every 3 days",
			from:     "2025-03-10",
			pattern:  model.RecurrencePattern{Type: model.PatternDaily, Every: 3, Mode: "fixed"},
			expected: "2025-03-13",
		},

		// --- Daily Weekday ---
		{
			name:     "weekday from Friday → Monday",
			from:     "2025-03-14", // Friday
			pattern:  model.RecurrencePattern{Type: model.PatternDailyWeekday, Every: 1, Mode: "fixed"},
			expected: "2025-03-17", // Monday
		},
		{
			name:     "weekday from Wednesday → Thursday",
			from:     "2025-03-12", // Wednesday
			pattern:  model.RecurrencePattern{Type: model.PatternDailyWeekday, Every: 1, Mode: "fixed"},
			expected: "2025-03-13", // Thursday
		},
		{
			name:     "weekday every 2 from Thursday → Monday (skip Friday)",
			from:     "2025-03-13", // Thursday
			pattern:  model.RecurrencePattern{Type: model.PatternDailyWeekday, Every: 2, Mode: "fixed"},
			expected: "2025-03-17", // Monday
		},

		// --- Daily Weekend ---
		{
			name:     "weekend from Friday → Saturday",
			from:     "2025-03-14", // Friday
			pattern:  model.RecurrencePattern{Type: model.PatternDailyWeekend, Every: 1, Mode: "fixed"},
			expected: "2025-03-15", // Saturday
		},
		{
			name:     "weekend from Saturday → Sunday",
			from:     "2025-03-15", // Saturday
			pattern:  model.RecurrencePattern{Type: model.PatternDailyWeekend, Every: 1, Mode: "fixed"},
			expected: "2025-03-16", // Sunday
		},
		{
			name:     "weekend from Sunday → Saturday",
			from:     "2025-03-16", // Sunday
			pattern:  model.RecurrencePattern{Type: model.PatternDailyWeekend, Every: 1, Mode: "fixed"},
			expected: "2025-03-22", // next Saturday
		},

		// --- Weekly ---
		{
			name:     "weekly no days → 7 days later",
			from:     "2025-03-10",
			pattern:  model.RecurrencePattern{Type: model.PatternWeekly, Every: 1, Mode: "fixed"},
			expected: "2025-03-17",
		},
		{
			name:     "weekly every 2 weeks no days",
			from:     "2025-03-10",
			pattern:  model.RecurrencePattern{Type: model.PatternWeekly, Every: 2, Mode: "fixed"},
			expected: "2025-03-24",
		},
		{
			name:     "weekly on mon,wed,fri from Monday → Wednesday same week",
			from:     "2025-03-10", // Monday
			pattern:  model.RecurrencePattern{Type: model.PatternWeekly, Every: 1, Mode: "fixed", On: []string{"mon", "wed", "fri"}},
			expected: "2025-03-12", // Wednesday
		},
		{
			name:     "weekly on mon,wed from Friday → Monday next week",
			from:     "2025-03-14", // Friday
			pattern:  model.RecurrencePattern{Type: model.PatternWeekly, Every: 1, Mode: "fixed", On: []string{"mon", "wed"}},
			expected: "2025-03-17", // Monday
		},
		{
			name:     "weekly every 2 on mon from Friday → Monday 2 weeks later",
			from:     "2025-03-14", // Friday
			pattern:  model.RecurrencePattern{Type: model.PatternWeekly, Every: 2, Mode: "fixed", On: []string{"mon"}},
			expected: "2025-03-24", // Monday, 2 weeks from week of Mar 10
		},

		// --- Monthly DOM ---
		{
			name:     "monthly dom null → same day next month",
			from:     "2025-03-15",
			pattern:  model.RecurrencePattern{Type: model.PatternMonthlyDOM, Every: 1, Mode: "fixed"},
			expected: "2025-04-15",
		},
		{
			name:     "monthly dom specific day 20",
			from:     "2025-03-10",
			pattern:  model.RecurrencePattern{Type: model.PatternMonthlyDOM, Every: 1, Mode: "fixed", Day: intPtr(20)},
			expected: "2025-04-20",
		},
		{
			name:     "monthly dom day 31 in April → clamped to 30",
			from:     "2025-03-10",
			pattern:  model.RecurrencePattern{Type: model.PatternMonthlyDOM, Every: 1, Mode: "fixed", Day: intPtr(31)},
			expected: "2025-04-30",
		},
		{
			name:     "monthly dom day 0 (last day)",
			from:     "2025-03-10",
			pattern:  model.RecurrencePattern{Type: model.PatternMonthlyDOM, Every: 1, Mode: "fixed", Day: intPtr(0)},
			expected: "2025-04-30",
		},
		{
			name:     "monthly dom day -1 (second to last)",
			from:     "2025-03-10",
			pattern:  model.RecurrencePattern{Type: model.PatternMonthlyDOM, Every: 1, Mode: "fixed", Day: intPtr(-1)},
			expected: "2025-04-29",
		},
		{
			name:     "monthly dom every 2 months",
			from:     "2025-01-15",
			pattern:  model.RecurrencePattern{Type: model.PatternMonthlyDOM, Every: 2, Mode: "fixed", Day: intPtr(15)},
			expected: "2025-03-15",
		},

		// --- Monthly DOW ---
		{
			name:     "monthly first Monday",
			from:     "2025-03-10",
			pattern:  model.RecurrencePattern{Type: model.PatternMonthlyDOW, Every: 1, Mode: "fixed", Ordinal: "first", Weekday: "monday"},
			expected: "2025-04-07", // first Monday of April 2025
		},
		{
			name:     "monthly last Friday",
			from:     "2025-03-10",
			pattern:  model.RecurrencePattern{Type: model.PatternMonthlyDOW, Every: 1, Mode: "fixed", Ordinal: "last", Weekday: "friday"},
			expected: "2025-04-25", // last Friday of April 2025
		},
		{
			name:     "monthly third Wednesday",
			from:     "2025-03-10",
			pattern:  model.RecurrencePattern{Type: model.PatternMonthlyDOW, Every: 1, Mode: "fixed", Ordinal: "third", Weekday: "wednesday"},
			expected: "2025-04-16", // third Wednesday of April 2025
		},

		// --- Monthly Workday ---
		{
			name:     "monthly first workday",
			from:     "2025-03-10",
			pattern:  model.RecurrencePattern{Type: model.PatternMonthlyWorkday, Every: 1, Mode: "fixed", WorkdayPosition: "first"},
			expected: "2025-04-01", // April 1, 2025 is Tuesday
		},
		{
			name:     "monthly last workday",
			from:     "2025-03-10",
			pattern:  model.RecurrencePattern{Type: model.PatternMonthlyWorkday, Every: 1, Mode: "fixed", WorkdayPosition: "last"},
			expected: "2025-04-30", // April 30, 2025 is Wednesday
		},
		{
			name:     "monthly first workday when 1st is Saturday",
			from:     "2025-01-10",
			pattern:  model.RecurrencePattern{Type: model.PatternMonthlyWorkday, Every: 1, Mode: "fixed", WorkdayPosition: "first"},
			expected: "2025-02-03", // Feb 1, 2025 is Saturday → first workday is Monday Feb 3
		},

		// --- Yearly Date ---
		{
			name:     "yearly same date",
			from:     "2025-03-15",
			pattern:  model.RecurrencePattern{Type: model.PatternYearlyDate, Every: 1, Mode: "fixed", Month: 3, Day: intPtr(15)},
			expected: "2026-03-15",
		},
		{
			name:     "yearly specific date",
			from:     "2025-03-10",
			pattern:  model.RecurrencePattern{Type: model.PatternYearlyDate, Every: 1, Mode: "fixed", Month: 7, Day: intPtr(4)},
			expected: "2026-07-04",
		},
		{
			name:     "yearly every 2 years",
			from:     "2025-03-10",
			pattern:  model.RecurrencePattern{Type: model.PatternYearlyDate, Every: 2, Mode: "fixed", Month: 6, Day: intPtr(1)},
			expected: "2027-06-01",
		},
		{
			name:     "yearly Feb 29 on non-leap year → clamped to Feb 28",
			from:     "2024-02-29",
			pattern:  model.RecurrencePattern{Type: model.PatternYearlyDate, Every: 1, Mode: "fixed", Month: 2, Day: intPtr(29)},
			expected: "2025-02-28", // 2025 is not a leap year; Feb 29 clamps to Feb 28
		},

		// --- Yearly DOW ---
		{
			name:     "yearly first Monday of September",
			from:     "2025-03-10",
			pattern:  model.RecurrencePattern{Type: model.PatternYearlyDOW, Every: 1, Mode: "fixed", Month: 9, Ordinal: "first", Weekday: "monday"},
			expected: "2026-09-07", // first Monday of September 2026
		},
		{
			name:     "yearly last Thursday of November (Thanksgiving)",
			from:     "2025-03-10",
			pattern:  model.RecurrencePattern{Type: model.PatternYearlyDOW, Every: 1, Mode: "fixed", Month: 11, Ordinal: "fourth", Weekday: "thursday"},
			expected: "2026-11-26", // fourth Thursday of November 2026
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.Next(tt.from, tt.pattern)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("got %s, want %s", result, tt.expected)
			}
		})
	}
}

func TestEngineUnknownType(t *testing.T) {
	engine := NewEngine()
	_, err := engine.Next("2025-03-10", model.RecurrencePattern{Type: "bogus"})
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestWeeklyIntervalBugFix(t *testing.T) {
	// Regression test: the old nextMatchingDay() ignored interval for weekly rules.
	// With every=2 and on=["mon"], from a Friday, it should skip to 2 weeks later, not next Monday.
	engine := NewEngine()
	result, err := engine.Next("2025-03-14", model.RecurrencePattern{
		Type:  model.PatternWeekly,
		Every: 2,
		Mode:  "fixed",
		On:    []string{"mon"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Mar 14 is Friday. Week starts Mon Mar 10. 2 weeks later is Mon Mar 24.
	if result != "2025-03-24" {
		t.Errorf("got %s, want 2025-03-24", result)
	}
}

func TestNthWeekdayClamp(t *testing.T) {
	// February 2025 has 4 Mondays (3, 10, 17, 24). A "fourth" Monday works, but
	// if we asked for a 5th it should clamp to last.
	result := nthWeekdayInMonth(2025, 2, "fourth", 1) // 1 = Monday
	if result.Day() != 24 {
		t.Errorf("4th Monday of Feb 2025: got day %d, want 24", result.Day())
	}
}

func TestMonthlyDOM_FromJan31(t *testing.T) {
	// from.AddDate(0, every, 0) from Jan 31 lands in March, skipping February.
	engine := NewEngine()

	tests := []struct {
		name     string
		from     string
		expected string
	}{
		{"day 31 from Jan 31 → Feb 28", "2026-01-31", "2026-02-28"},
		{"day 31 from Jan 31 leap year → Feb 29", "2028-01-31", "2028-02-29"},
		{"day 31 from Feb 28 → Mar 31", "2026-02-28", "2026-03-31"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.Next(tt.from, model.RecurrencePattern{
				Type:  model.PatternMonthlyDOM,
				Every: 1,
				Mode:  "fixed",
				Day:   intPtr(31),
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("got %s, want %s", result, tt.expected)
			}
		})
	}
}

func TestMonthlyDOW_FromJan31(t *testing.T) {
	// from.AddDate(0, every, 0) from Jan 31 lands in March, skipping February.
	engine := NewEngine()

	result, err := engine.Next("2026-01-31", model.RecurrencePattern{
		Type:    model.PatternMonthlyDOW,
		Every:   1,
		Mode:    "fixed",
		Ordinal: "first",
		Weekday: "monday",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := time.Parse("2006-01-02", result)
	if err != nil {
		t.Fatalf("unparseable result %q: %v", result, err)
	}
	if got.Year() != 2026 || got.Month() != time.February {
		t.Errorf("got %s, want a date in February 2026", result)
	}
}

func TestMonthlyWorkday_FromJan31(t *testing.T) {
	// from.AddDate(0, every, 0) from Jan 31 lands in March, skipping February.
	engine := NewEngine()

	result, err := engine.Next("2026-01-31", model.RecurrencePattern{
		Type:            model.PatternMonthlyWorkday,
		Every:           1,
		Mode:            "fixed",
		WorkdayPosition: "first",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := time.Parse("2006-01-02", result)
	if err != nil {
		t.Fatalf("unparseable result %q: %v", result, err)
	}
	if got.Year() != 2026 || got.Month() != time.February {
		t.Errorf("got %s, want a date in February 2026", result)
	}
}

func TestYearlyFeb29_EveryOne(t *testing.T) {
	engine := NewEngine()

	result, err := engine.Next("2027-03-01", model.RecurrencePattern{
		Type:  model.PatternYearlyDate,
		Every: 1,
		Mode:  "fixed",
		Month: 2,
		Day:   intPtr(29),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "2028-02-29" {
		t.Errorf("got %s, want 2028-02-29", result)
	}
}

func TestFirstOnOrAfter_Feb29NonLeap(t *testing.T) {
	// In a non-leap year, Feb 29 clamps to Feb 28 of the current year, but only
	// when the input is on or before Feb 28; a later input moves to the next
	// occurrence. FirstOnOrAfter never returns a date before its input.
	engine := NewEngine()

	tests := []struct {
		name     string
		from     string
		expected string
	}{
		{"before Feb 28 → clamped Feb 28 this year", "2026-02-10", "2026-02-28"},
		{"on Feb 28 → Feb 28 this year", "2026-02-28", "2026-02-28"},
		{"after Feb 28 → Feb 28 next year", "2026-03-01", "2027-02-28"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.FirstOnOrAfter(tt.from, model.RecurrencePattern{
				Type:  model.PatternYearlyDate,
				Every: 1,
				Mode:  "fixed",
				Month: 2,
				Day:   intPtr(29),
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("got %s, want %s", result, tt.expected)
			}
		})
	}
}

func TestYearlyFeb29_EveryFour(t *testing.T) {
	// With every=4 from 2030, the old leap-seeking loop never reaches a leap
	// year (2034, 2038, ... are all ≡ 2 mod 4) and Next never returns.
	// Guard: fail the test if it does not return within 1 s.
	engine := NewEngine()

	timer := time.AfterFunc(time.Second, func() {
		panic("TestYearlyFeb29_EveryFour: Next did not return within 1s (infinite loop)")
	})
	defer timer.Stop()

	result, err := engine.Next("2030-02-28", model.RecurrencePattern{
		Type:  model.PatternYearlyDate,
		Every: 4,
		Mode:  "fixed",
		Month: 2,
		Day:   intPtr(29),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "2034-02-28" {
		t.Errorf("got %s, want 2034-02-28", result)
	}
}
