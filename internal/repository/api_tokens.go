package repository

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/collinjanssen/thingstodo/internal/model"
)

var (
	ErrDuplicateTokenName = fmt.Errorf("duplicate token name")
	ErrTokenNotFound      = fmt.Errorf("api token not found")
)

// Token value layout: the literal prefix "ttd_" followed by 32
// crypto-random bytes base64url-encoded (43 chars, no padding). Only the
// hex-encoded SHA-256 of the full value is stored; lookups hash the
// presented token and resolve it through the UNIQUE index on token_hash.
const (
	tokenValuePrefix = "ttd_"
	tokenEntropy     = 32
	tokenPrefixLen   = 8
	// touchInterval throttles last_used_at writes to one per token per
	// minute, keeping token-authenticated request paths write-free.
	touchInterval = time.Minute
	// timestampLayout matches the datetime('now') storage layout used
	// across the schema.
	timestampLayout = "2006-01-02 15:04:05"
)

// hashToken returns the hex-encoded SHA-256 of a token value.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type APITokenRepository struct {
	db *sql.DB
	// lastWrite throttles last_used_at writes; guarded by mu.
	mu        sync.Mutex
	lastWrite map[string]time.Time
	now       func() time.Time
}

func NewAPITokenRepository(db *sql.DB) *APITokenRepository {
	return &APITokenRepository{
		db:        db,
		lastWrite: make(map[string]time.Time),
		now:       time.Now,
	}
}

// WithClock overrides the clock used by TouchLastUsed (tests).
func (r *APITokenRepository) WithClock(now func() time.Time) *APITokenRepository {
	if now != nil {
		r.now = now
	}
	return r
}

// newTokenValue returns a fresh token value: "ttd_" plus 32 crypto-random
// bytes base64url-encoded.
func newTokenValue() string {
	b := make([]byte, tokenEntropy)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return tokenValuePrefix + base64.RawURLEncoding.EncodeToString(b)
}

// TokenPrefix returns the display prefix of a token value: its first 8
// characters ("ttd_" plus 4), so logs and lists can identify a token
// without revealing it.
func TokenPrefix(token string) string {
	if len(token) < tokenPrefixLen {
		return token
	}
	return token[:tokenPrefixLen]
}

// Create stores a new named token and returns its row id and the plaintext
// token value. The plaintext is returned exactly once and never stored; a
// token-hash collision (or id collision) retries with fresh entropy.
func (r *APITokenRepository) Create(name string) (id, token string, err error) {
	for attempt := 0; attempt < 5; attempt++ {
		token = newTokenValue()
		id = model.NewID()
		_, err = r.db.Exec(
			`INSERT INTO api_tokens (id, name, token_hash, prefix) VALUES (?, ?, ?, ?)`,
			id, name, hashToken(token), TokenPrefix(token),
		)
		if err == nil {
			return id, token, nil
		}
		if strings.Contains(err.Error(), "UNIQUE constraint failed: api_tokens.name") {
			return "", "", ErrDuplicateTokenName
		}
		if isUniqueConstraintError(err) {
			continue
		}
		return "", "", fmt.Errorf("create api token: %w", err)
	}
	return "", "", fmt.Errorf("create api token: retries exhausted")
}

// List returns every token, newest first. Hash values are never selected.
func (r *APITokenRepository) List() ([]model.APIToken, error) {
	rows, err := r.db.Query(`
		SELECT id, name, prefix, created_at, last_used_at, revoked_at
		FROM api_tokens
		ORDER BY created_at DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	defer func() { _ = rows.Close() }()

	tokens := []model.APIToken{}
	for rows.Next() {
		var t model.APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.CreatedAt, &t.LastUsedAt, &t.RevokedAt); err != nil {
			return nil, fmt.Errorf("scan api token: %w", err)
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// Revoke marks a token revoked. Revoking an already-revoked token is a
// no-op; an unknown id returns ErrTokenNotFound.
func (r *APITokenRepository) Revoke(id string) error {
	res, err := r.db.Exec(
		`UPDATE api_tokens SET revoked_at = datetime('now') WHERE id = ? AND revoked_at IS NULL`,
		id,
	)
	if err != nil {
		return fmt.Errorf("revoke api token: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		var exists int
		if err := r.db.QueryRow(`SELECT COUNT(*) FROM api_tokens WHERE id = ?`, id).Scan(&exists); err != nil {
			return fmt.Errorf("revoke api token: %w", err)
		}
		if exists == 0 {
			return ErrTokenNotFound
		}
	}
	return nil
}

// Lookup resolves a plaintext token value to its row id. Only active
// (non-revoked) rows match. The presented token is hashed and resolved via
// the UNIQUE index on token_hash; unknown values return ok=false.
func (r *APITokenRepository) Lookup(token string) (id string, ok bool, err error) {
	err = r.db.QueryRow(
		`SELECT id FROM api_tokens WHERE token_hash = ? AND revoked_at IS NULL`,
		hashToken(token),
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("lookup api token: %w", err)
	}
	return id, true, nil
}

// TouchLastUsed updates last_used_at for a token, at most once per token
// per minute (an in-memory last-write map decides; the clock is
// injectable for tests). Failures are logged and swallowed: last_used_at
// is telemetry and must not fail the request.
func (r *APITokenRepository) TouchLastUsed(id string) {
	r.mu.Lock()
	now := r.now()
	if last, ok := r.lastWrite[id]; ok && now.Sub(last) < touchInterval {
		r.mu.Unlock()
		return
	}
	r.lastWrite[id] = now
	r.mu.Unlock()

	if _, err := r.db.Exec(
		`UPDATE api_tokens SET last_used_at = ? WHERE id = ? AND revoked_at IS NULL`,
		now.UTC().Format(timestampLayout), id,
	); err != nil {
		log.Printf("ERROR touch api token last_used_at id=%s: %v", id, err)
	}
}
