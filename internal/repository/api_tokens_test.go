package repository_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/testutil"
)

func TestTokenLastUsed_Throttled(t *testing.T) {
	db := testutil.SetupTestDB(t)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	repo := repository.NewAPITokenRepository(db).WithClock(func() time.Time { return now })

	id, _, err := repo.Create("throttled")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	lastUsed := func() string {
		t.Helper()
		var lu sql.NullString
		if err := db.QueryRow(`SELECT last_used_at FROM api_tokens WHERE id = ?`, id).Scan(&lu); err != nil {
			t.Fatalf("read last_used_at: %v", err)
		}
		return lu.String
	}

	// The first touch writes the (injected) clock time.
	repo.TouchLastUsed(id)
	if got := lastUsed(); got != "2026-10-17 12:00:00" {
		t.Fatalf("expected first write to record 2026-10-17 12:00:00, got %q", got)
	}

	// A second touch within the minute leaves exactly one write.
	now = now.Add(30 * time.Second)
	repo.TouchLastUsed(id)
	if got := lastUsed(); got != "2026-10-17 12:00:00" {
		t.Fatalf("expected the throttled write to keep 2026-10-17 12:00:00, got %q", got)
	}

	// Past the interval the write goes through again.
	now = now.Add(31 * time.Second)
	repo.TouchLastUsed(id)
	if got := lastUsed(); got != "2026-10-17 12:01:01" {
		t.Fatalf("expected the post-interval write to record 2026-10-17 12:01:01, got %q", got)
	}
}
