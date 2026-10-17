package handler

import (
	"errors"
	"regexp"
	"time"

	"github.com/collinjanssen/thingstodo/internal/model"
)

var (
	clockRE    = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
	clientIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{8,21}$`)
)

// isDate reports whether s is a real calendar date in YYYY-MM-DD form.
// time.Parse alone accepts 2026-02-31 (it normalizes overflow into the next
// month), so the parsed value must also format back to the input unchanged.
func isDate(s string) bool {
	t, err := time.Parse("2006-01-02", s)
	return err == nil && t.Format("2006-01-02") == s
}

// isWhenDate reports whether s is a valid when date: a real calendar date or
// the special value "someday".
func isWhenDate(s string) bool {
	return s == "someday" || isDate(s)
}

// isClock reports whether s is a valid 24-hour HH:MM time.
func isClock(s string) bool {
	return clockRE.MatchString(s)
}

// isClientID reports whether s is an acceptable client-supplied entity id:
// 8-21 characters of letters, digits, underscore or hyphen.
func isClientID(s string) bool {
	return clientIDRE.MatchString(s)
}

// normalizeExactAt parses an exact reminder time and returns it as UTC
// RFC 3339. It accepts RFC 3339 with a numeric offset or Z, or a naive
// 2006-01-02T15:04[:05] timestamp read in loc.
func normalizeExactAt(s string, loc *time.Location) (string, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t.UTC().Format(time.RFC3339), nil
		}
	}
	return "", errors.New("must be RFC 3339 with an offset or Z, or YYYY-MM-DDTHH:MM[:SS]")
}

// reminderTypes is the set of reminder types accepted at the boundary.
var reminderTypes = map[model.ReminderType]bool{
	model.ReminderAtStart:       true,
	model.ReminderOnDay:         true,
	model.ReminderMinutesBefore: true,
	model.ReminderHoursBefore:   true,
	model.ReminderDaysBefore:    true,
	model.ReminderExact:         true,
}

// validateReminderCreate applies the reminder rules shared by
// POST /api/tasks/:id/reminders and sync reminder creates: a known type, a
// value in 0-99, and a required exact_at for the exact type. A valid
// exact_at is normalized in place to UTC RFC 3339 (naive values are read
// in loc). It returns the failing field and message.
func validateReminderCreate(input *model.CreateReminderInput, loc *time.Location) (string, string, bool) {
	if !reminderTypes[input.Type] {
		return "type", "invalid reminder type", false
	}
	if input.Value < 0 || input.Value > 99 {
		return "value", "must be between 0 and 99", false
	}
	if input.Type == model.ReminderExact {
		if input.ExactAt == nil {
			return "exact_at", "is required for the exact type", false
		}
		normalized, err := normalizeExactAt(*input.ExactAt, loc)
		if err != nil {
			return "exact_at", err.Error(), false
		}
		input.ExactAt = &normalized
	}
	return "", "", true
}

var (
	sortOrders            = map[string]bool{"manual": true, "a-z": true, "z-a": true}
	timeFormats           = map[string]bool{"12h": true, "24h": true}
	fontSizes             = map[int]bool{12: true, 14: true, 16: true, 18: true, 20: true}
	notificationProviders = map[string]bool{"webpush": true, "ntfy": true, "none": true}
	defaultReminderTypes  = map[string]bool{"at_start": true, "on_day": true, "minutes_before": true, "hours_before": true, "days_before": true}
	projectStatuses       = map[string]bool{"open": true, "completed": true, "canceled": true}
)

// validateSettingsPatch applies the enum and range rules documented for
// PATCH /api/user/settings. Fields that are nil are not part of the patch
// and stay allowed (including default_reminder_type null, which clears).
func validateSettingsPatch(input *model.UpdateUserSettingsInput) (string, string, bool) {
	if input.SortAreas != nil && !sortOrders[*input.SortAreas] {
		return "sort_areas", "must be manual, a-z or z-a", false
	}
	if input.SortTags != nil && !sortOrders[*input.SortTags] {
		return "sort_tags", "must be manual, a-z or z-a", false
	}
	if input.EveningStartsAt != nil && !isClock(*input.EveningStartsAt) {
		return "evening_starts_at", "must be HH:MM in 24-hour form", false
	}
	if input.TimeFormat != nil && !timeFormats[*input.TimeFormat] {
		return "time_format", "must be 12h or 24h", false
	}
	if input.FontSize != nil && !fontSizes[*input.FontSize] {
		return "font_size", "must be one of 12, 14, 16, 18 or 20", false
	}
	if input.DefaultReminderType != nil && !defaultReminderTypes[*input.DefaultReminderType] {
		return "default_reminder_type", "must be at_start, on_day, minutes_before, hours_before or days_before", false
	}
	if input.DefaultReminderValue != nil && (*input.DefaultReminderValue < 0 || *input.DefaultReminderValue > 99) {
		return "default_reminder_value", "must be between 0 and 99", false
	}
	if input.NotificationProvider != nil && !notificationProviders[*input.NotificationProvider] {
		return "notification_provider", "must be webpush, ntfy or none", false
	}
	return "", "", true
}

// isProjectStatus reports whether s is a valid project status.
func isProjectStatus(s string) bool {
	return projectStatuses[s]
}
