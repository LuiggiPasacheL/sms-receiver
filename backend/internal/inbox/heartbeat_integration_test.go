package inbox

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresHeartbeatPersistenceAndNotification(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	// Isolate the singleton heartbeat record from other integration fixtures.
	schema := pgx.Identifier{fmt.Sprintf("heartbeat_test_%d", time.Now().UnixNano())}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := Store{Pool: pool}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if seen, err := store.LastHeartbeat(ctx); err != nil || seen != nil {
		t.Fatalf("initial heartbeat=%v error=%v", seen, err)
	}
	if _, err := conn.Exec(ctx, "LISTEN sms_messages"); err != nil {
		t.Fatal(err)
	}
	first, err := store.RecordHeartbeat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wait, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	if _, err := conn.WaitForNotification(wait); err != nil {
		t.Fatalf("heartbeat did not notify browsers: %v", err)
	}
	second, err := store.RecordHeartbeat(ctx)
	if err != nil || second.Before(first) {
		t.Fatalf("updated heartbeat=%v first=%v error=%v", second, first, err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// Another store (as on a different API replica) reads the persisted ping.
	seen, err := (Store{Pool: pool}).LastHeartbeat(ctx)
	if err != nil || seen == nil || !seen.Equal(second) {
		t.Fatalf("persisted heartbeat=%v want=%v error=%v", seen, second, err)
	}
	var heartbeats, messages int
	if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM forwarder_heartbeat), (SELECT count(*) FROM messages)").Scan(&heartbeats, &messages); err != nil {
		t.Fatal(err)
	}
	if heartbeats != 1 || messages != 0 {
		t.Fatalf("heartbeats=%d messages=%d", heartbeats, messages)
	}
}
