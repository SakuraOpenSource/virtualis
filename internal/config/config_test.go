package config

import (
	"strings"
	"testing"
)

// SC-02: PostgreSQL must default to sslmode=prefer (negotiate TLS when
// offered) instead of the previous hardcoded disable that sent credentials
// in the clear; explicit configuration wins over the default.
func TestPostgresDSNDefaultSSLModePrefer(t *testing.T) {
	cfg := Database{Driver: DriverPostgres, Host: "db.local", Port: 5432, User: "u", Password: "p", Name: "n"}
	dsn, err := cfg.DSN()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dsn, "sslmode=prefer") {
		t.Fatalf("default sslmode not prefer: %s", dsn)
	}
	cfg.SSLMode = "verify-full"
	if dsn, _ = cfg.DSN(); !strings.Contains(dsn, "sslmode=verify-full") {
		t.Fatalf("explicit sslmode ignored: %s", dsn)
	}
	// MySQL: unset keeps the driver default (backwards compatible).
	cfg = Database{Driver: DriverMySQL, Host: "db.local", Port: 3306, User: "u", Password: "p", Name: "n"}
	if dsn, _ = cfg.DSN(); strings.Contains(dsn, "tls=") {
		t.Fatalf("mysql dsn grew a tls param without configuration: %s", dsn)
	}
	cfg.SSLMode = "true"
	if dsn, _ = cfg.DSN(); !strings.Contains(dsn, "tls=true") {
		t.Fatalf("mysql tls config ignored: %s", dsn)
	}
}
