package inbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresSIMAliasesAndBulkDeletion(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := Store{Pool: pool}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tag := fmt.Sprintf("sim-test-%d", time.Now().UnixNano())
	simID, otherID := tag+" /+%", tag+" /+%2"
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM messages WHERE sender = $1", tag)
		_, _ = pool.Exec(context.Background(), "DELETE FROM sim_aliases WHERE sim = ANY($1)", []string{simID, otherID})
	}()
	insert := func(id string) {
		t.Helper()
		if _, _, err := store.Insert(ctx, Incoming{From: tag, Text: "same sender, separate SIMs", SIM: Scalar(strconv.Quote(id))}, "test"); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < 55; index++ {
		insert(simID)
	}
	insert(otherID)
	insert(otherID)

	listener, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close(context.Background())
	if _, err := listener.Exec(ctx, "LISTEN sms_messages"); err != nil {
		t.Fatal(err)
	}
	assertNotification := func() {
		t.Helper()
		notifyCtx, stop := context.WithTimeout(ctx, 3*time.Second)
		defer stop()
		notification, err := listener.WaitForNotification(notifyCtx)
		if err != nil || notification.Channel != "sms_messages" {
			t.Fatalf("SIM mutation did not notify other instances: %+v %v", notification, err)
		}
	}
	assertSIM := func(id, alias string, count int64) {
		t.Helper()
		sims, err := store.SIMs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, sim := range sims {
			if sim.ID == id {
				if sim.Alias != alias || sim.MessageCount != count {
					t.Fatalf("SIM=%+v want alias=%q count=%d", sim, alias, count)
				}
				return
			}
		}
		t.Fatalf("missing SIM %q", id)
	}
	assertSIM(simID, "", 55)
	if err := store.SetSIMAlias(ctx, simID, "Personal ✓"); err != nil {
		t.Fatal(err)
	}
	assertNotification()
	// Identical aliases never merge two underlying SIM identifiers.
	if err := store.SetSIMAlias(ctx, otherID, "Personal ✓"); err != nil {
		t.Fatal(err)
	}
	assertNotification()
	assertSIM(simID, "Personal ✓", 55)
	assertSIM(otherID, "Personal ✓", 2)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	assertSIM(simID, "Personal ✓", 55)
	history, err := store.List(ctx, Filter{SIM: simID, From: tag, Limit: 50})
	if err != nil || history.Total != 55 || len(history.Messages) != 50 || history.Messages[0].SIM != simID {
		t.Fatalf("alias changed raw SIM or pagination: %+v %v", history, err)
	}
	if err := store.SetSIMAlias(ctx, simID, ""); err != nil {
		t.Fatal(err)
	}
	assertNotification()
	assertSIM(simID, "", 55)
	if err := store.SetSIMAlias(ctx, simID, "Work"); err != nil {
		t.Fatal(err)
	}
	assertNotification()
	// Force deletion to fail after reaching the alias row. Message deletion must
	// roll back too, so cancellation cannot leave a partially deleted SIM.
	locker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Rollback(context.Background())
	if _, err := locker.Exec(ctx, "SELECT sim FROM sim_aliases WHERE sim = $1 FOR UPDATE", simID); err != nil {
		t.Fatal(err)
	}
	blockedCtx, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	_, deleteErr := store.DeleteSIM(blockedCtx, simID)
	stop()
	if deleteErr == nil {
		t.Fatal("deletion unexpectedly succeeded while alias row was locked")
	}
	if err := locker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	assertSIM(simID, "Work", 55)
	count, err := store.DeleteSIM(ctx, simID)
	if err != nil || count != 55 {
		t.Fatalf("bulk delete count=%d err=%v", count, err)
	}
	assertNotification()
	sims, err := store.SIMs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sim := range sims {
		if sim.ID == simID {
			t.Fatalf("deleted SIM remains: %+v", sim)
		}
	}
	assertSIM(otherID, "Personal ✓", 2)
	history, err = store.List(ctx, Filter{From: tag, Limit: 100})
	if err != nil || history.Total != 2 || len(history.Messages) != 2 {
		t.Fatalf("deleted wrong messages: %+v %v", history, err)
	}
	for _, message := range history.Messages {
		if message.SIM != otherID {
			t.Fatalf("deleted SIM history survived: %+v", message)
		}
	}
	var aliases int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM sim_aliases WHERE sim = $1", simID).Scan(&aliases); err != nil || aliases != 0 {
		t.Fatalf("alias was not removed: %d %v", aliases, err)
	}
	if _, err := store.DeleteSIM(ctx, simID); !errors.Is(err, ErrSIMNotFound) {
		t.Fatalf("deleting unknown SIM: %v", err)
	}
	if err := store.SetSIMAlias(ctx, simID, "Missing"); !errors.Is(err, ErrSIMNotFound) {
		t.Fatalf("renaming unknown SIM: %v", err)
	}
	insert(simID)
	assertSIM(simID, "", 1)
}
