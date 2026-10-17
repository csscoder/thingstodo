package database

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func Open(dbPath string) (*sql.DB, error) {
	// Ensure the directory exists.
	if dir := filepath.Dir(dbPath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}

	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	if err := runMigrations(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return db, nil
}

// CheckScheduleTimeFormats logs every task_schedules start_time and
// end_time that still fails the HH:MM shape after migration 029 zero-padded
// single-digit hours. It only reports; it never modifies rows and never
// exits, so a stray legacy value cannot keep the server from starting.
func CheckScheduleTimeFormats(db *sql.DB) {
	rows, err := db.Query(`
		SELECT id, 'start_time', start_time FROM task_schedules
			WHERE start_time IS NOT NULL AND start_time NOT GLOB '[0-9][0-9]:[0-5][0-9]'
		UNION ALL
		SELECT id, 'end_time', end_time FROM task_schedules
			WHERE end_time IS NOT NULL AND end_time NOT GLOB '[0-9][0-9]:[0-5][0-9]'
		ORDER BY 1`)
	if err != nil {
		log.Printf("WARNING: schedule time format check failed: %v", err)
		return
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, field, value string
		if err := rows.Scan(&id, &field, &value); err != nil {
			log.Printf("WARNING: schedule time format check scan: %v", err)
			return
		}
		log.Printf("WARNING: task_schedules %s has %s %q that is not HH:MM; leaving it unchanged", id, field, value)
	}
}

func runMigrations(db *sql.DB) error {
	// Create migrations tracking table.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS _migrations (
		name TEXT PRIMARY KEY,
		applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	// Sort by filename to ensure order.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		name := entry.Name()

		// Check if already applied.
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM _migrations WHERE name = ?", name).Scan(&count); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if count > 0 {
			continue
		}

		content, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		sql := strings.TrimSpace(string(content))
		if sql == "" || strings.HasPrefix(sql, "--") && !strings.Contains(sql, "\n") {
			// Skip comment-only migrations but still record them.
			log.Printf("migration %s: skipped (comment only)", name)
		} else {
			if _, err := db.Exec(sql); err != nil {
				return fmt.Errorf("execute migration %s: %w", name, err)
			}
			log.Printf("migration %s: applied", name)
		}

		if _, err := db.Exec("INSERT INTO _migrations (name) VALUES (?)", name); err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}
	}

	return nil
}
