package config

import "testing"

func TestValidate_BuiltinRequiresJWTSecret(t *testing.T) {
	if err := (Config{AuthMode: "builtin"}).Validate(); err == nil {
		t.Error("expected error for builtin mode without JWTSecret, got nil")
	}
	if err := (Config{AuthMode: "builtin", JWTSecret: "s"}).Validate(); err != nil {
		t.Errorf("expected no error for builtin mode with JWTSecret, got %v", err)
	}
}

func TestValidate_OIDCRequiresAllowedEmail(t *testing.T) {
	if err := (Config{AuthMode: "oidc", JWTSecret: "s"}).Validate(); err == nil {
		t.Error("expected error for oidc mode without OIDCAllowedEmail, got nil")
	}
	if err := (Config{AuthMode: "oidc", JWTSecret: "s", OIDCAllowedEmail: "user@example.com"}).Validate(); err != nil {
		t.Errorf("expected no error for oidc mode with OIDCAllowedEmail, got %v", err)
	}
}
