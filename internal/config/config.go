package config

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port            int
	DBPath          string
	AuthMode        string
	LogLevel        string
	AttachmentsPath string
	MaxUploadSize   int64
	AuthProxyHeader string
	JWTSecret       string
	LoginPassword   string
	APIKey          string

	// OIDC (AUTH_MODE=oidc)
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURI  string
	// OIDCAllowedEmail is the single email allowed to log in (required in
	// oidc mode); it is also the canonical username of the app user.
	OIDCAllowedEmail string

	// VAPID (Web Push)
	VAPIDPrivateKey string
	VAPIDPublicKey  string
	VAPIDContact    string

	// Timezone for date/time calculations (IANA name, e.g. "America/Chicago")
	Location *time.Location
	// TimeZoneName is the IANA name reported to clients (resolved from the
	// TZ env var, then /etc/localtime, else "UTC").
	TimeZoneName string
}

func Load() Config {
	dbPath := envStr("DB_PATH", "./data/thingstodo.db")
	dataDir := filepath.Dir(dbPath)
	cfg := Config{
		Port:            envInt("PORT", 2999),
		DBPath:          dbPath,
		AuthMode:        envStr("AUTH_MODE", "builtin"),
		LogLevel:        envStr("LOG_LEVEL", "info"),
		AttachmentsPath: filepath.Join(dataDir, "attachments"),
		MaxUploadSize:   envInt64("MAX_UPLOAD_SIZE", 25*1024*1024),
		AuthProxyHeader: envStr("AUTH_PROXY_HEADER", "Remote-User"),
		JWTSecret:       envStr("JWT_SECRET", ""),
		LoginPassword:   envStr("LOGIN_PASSWORD", ""),
		APIKey:          envStr("API_KEY", ""),

		OIDCIssuer:       envStr("OIDC_ISSUER", ""),
		OIDCClientID:     envStr("OIDC_CLIENT_ID", ""),
		OIDCClientSecret: envStr("OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURI:  envStr("OIDC_REDIRECT_URI", ""),
		OIDCAllowedEmail: envStr("OIDC_ALLOWED_EMAIL", ""),

		VAPIDPrivateKey: envStr("VAPID_PRIVATE_KEY", ""),
		VAPIDPublicKey:  envStr("VAPID_PUBLIC_KEY", ""),
		VAPIDContact:    envStr("VAPID_CONTACT", ""),
	}

	if err := cfg.Validate(); err != nil {
		log.Fatal(err)
	}

	// Load timezone — defaults to system TZ (UTC in most Docker containers)
	tz := envStr("TZ", "")
	if tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			log.Fatalf("invalid TZ %q: %v", tz, err)
		}
		cfg.Location = loc
	} else {
		cfg.Location = time.Local
	}

	// Resolve the IANA name to report to clients: time.Local reports
	// "Local", which clients (e.g. Intl.DateTimeFormat) reject. The TZ-unset
	// path cannot be unit-tested without process-wide env manipulation, so
	// the resolution logic lives in zoneName and is covered by TestZoneName.
	cfg.TimeZoneName = zoneName(cfg.Location, func() (string, error) { return os.Readlink("/etc/localtime") })

	// When TZ is unset, make the server use the zone it reports: time.Local
	// may be a shallow zone, so reload the resolved name for the full zone
	// database entry. On failure keep time.Local.
	if tz == "" && cfg.TimeZoneName != "UTC" {
		if loc, err := time.LoadLocation(cfg.TimeZoneName); err == nil {
			cfg.Location = loc
		}
	}

	return cfg
}

// zoneName resolves the IANA name of loc. A named location (from TZ, or
// time.UTC) reports itself; time.Local reports "Local", so in that case the
// IANA name is taken from the path after the last "zoneinfo/" segment of the
// /etc/localtime symlink target. On any failure it falls back to "UTC".
func zoneName(loc *time.Location, localtimeTarget func() (string, error)) string {
	if name := loc.String(); name != "Local" {
		return name
	}
	target, err := localtimeTarget()
	if err != nil {
		return "UTC"
	}
	if i := strings.LastIndex(target, "zoneinfo/"); i >= 0 {
		return target[i+len("zoneinfo/"):]
	}
	return "UTC"
}

// Validate reports configuration errors that make the selected auth mode
// unusable. Load calls it and exits the process on failure.
func (c Config) Validate() error {
	if (c.AuthMode == "builtin" || c.AuthMode == "oidc") && c.JWTSecret == "" {
		return errors.New("JWT_SECRET must be set when AUTH_MODE is builtin or oidc")
	}
	if c.AuthMode == "oidc" && c.OIDCAllowedEmail == "" {
		return errors.New("OIDC_ALLOWED_EMAIL must be set when AUTH_MODE is oidc")
	}
	return nil
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envInt64(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return fallback
}
