// Package testdb gives tests a real Postgres: TEST_DATABASE_URL when set,
// otherwise an embedded server started once per test binary.
package testdb

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var dsn string

// Main runs the package's tests against a database and stops it afterwards.
func Main(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	if dsn = os.Getenv("TEST_DATABASE_URL"); dsn != "" {
		return m.Run()
	}
	dir, err := os.MkdirTemp("", "foods-pg")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	port, err := freePort()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cache := filepath.Join(os.TempDir(), "foods-embedded-postgres")
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Port(port).
		Encoding("UTF8").
		RuntimePath(filepath.Join(dir, "runtime")).
		DataPath(filepath.Join(dir, "data")).
		CachePath(cache).
		Logger(nil))
	if err := pg.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start embedded postgres:", err)
		return 1
	}
	defer func() {
		if err := pg.Stop(); err != nil {
			fmt.Fprintln(os.Stderr, "stop embedded postgres:", err)
		}
	}()
	dsn = fmt.Sprintf("host=localhost port=%d user=postgres password=postgres dbname=postgres sslmode=disable", port)
	return m.Run()
}

func freePort() (uint32, error) {
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return uint32(l.Addr().(*net.TCPAddr).Port), nil //nolint:gosec // a TCP port fits in uint32
}

// Open connects to the test database.
func Open(t testing.TB) *gorm.DB {
	t.Helper()
	if dsn == "" {
		t.Fatal("testdb.Main was not called from TestMain")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}
