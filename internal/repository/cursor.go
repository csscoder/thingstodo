package repository

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// ErrInvalidCursor marks a cursor token that is not one this server
// produced: handlers map it to 400 VALIDATION with field "cursor".
var ErrInvalidCursor = errors.New("invalid cursor")

// cursorPayload is the JSON carried inside the opaque cursor token.
type cursorPayload struct {
	K string `json:"k"`
	I string `json:"i"`
}

// encodeCursor packs the last returned row's sort key and id into an opaque
// base64url token. sortKey may be a string (dates, timestamps, titles) or a
// float (manual sort orders, search rank); floats are formatted losslessly
// so decodeCursor round-trips them exactly. The token is shared by the task
// list and search pagination.
func encodeCursor(sortKey any, id string) string {
	var k string
	switch v := sortKey.(type) {
	case string:
		k = v
	case float64:
		k = strconv.FormatFloat(v, 'g', -1, 64)
	default:
		k = fmt.Sprintf("%v", v)
	}
	b, err := json.Marshal(cursorPayload{K: k, I: id})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor unpacks a token produced by encodeCursor. Both the sort key
// and the id come back as strings; callers parse the key back into the type
// their sort uses (decodeFloatCursor for numeric keys such as manual sort
// orders and search ranks). Anything malformed is ErrInvalidCursor.
func decodeCursor(s string) (sortKey string, id string, err error) {
	if s == "" {
		return "", "", fmt.Errorf("%w: empty token", ErrInvalidCursor)
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	var p cursorPayload
	if err := json.Unmarshal(b, &p); err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	if p.I == "" {
		return "", "", fmt.Errorf("%w: missing id", ErrInvalidCursor)
	}
	return p.K, p.I, nil
}

// decodeFloatCursor parses the sort key of a cursor whose sort key was a
// float (manual sort orders, search rank).
func decodeFloatCursor(sortKey string) (float64, error) {
	f, err := strconv.ParseFloat(sortKey, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: sort key: %v", ErrInvalidCursor, err)
	}
	return f, nil
}
