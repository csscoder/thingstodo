package handler

import (
	"regexp"
	"time"
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
