package testdb

import (
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestWithDatabase(t *testing.T) {
	for _, base := range []string{
		"postgres://alice:secret@localhost:5432/foods_test?sslmode=disable",
		"postgresql://alice@localhost/foods_test?dbname=foods_test&sslmode=disable",
		"host=localhost port=5432 user=alice dbname=foods_test sslmode=disable",
		"host=localhost user=alice",
	} {
		got, err := withDatabase(base, "test_1")
		if err != nil {
			t.Fatalf("withDatabase(%q): %v", base, err)
		}
		cfg, err := pgconn.ParseConfig(got)
		if err != nil {
			t.Fatalf("parse %q: %v", got, err)
		}
		if cfg.Database != "test_1" || cfg.User != "alice" || cfg.Host != "localhost" {
			t.Errorf("withDatabase(%q) = %q: database=%q user=%q host=%q", base, got, cfg.Database, cfg.User, cfg.Host)
		}
	}
	if _, err := withDatabase("postgres://[::1", "test_1"); err == nil {
		t.Error("malformed DSN accepted")
	}
}
