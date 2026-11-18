package inbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresPersistencePaginationAndNotification(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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
	// Running migrations again is safe and does not lose records.
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	sender := fmt.Sprintf("integration-%d", time.Now().UnixNano())
	testSIM := sender + " SIM"
	defer func() { _, _ = pool.Exec(context.Background(), "DELETE FROM messages WHERE sender=$1", sender) }()
	listener, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close(context.Background())
	if _, err := listener.Exec(ctx, "LISTEN sms_messages"); err != nil {
		t.Fatal(err)
	}
	in := Incoming{From: sender, Text: "A literal 100%_ message ✓", SentStamp: Scalar(`1794528000000`), ReceivedStamp: Scalar(`"2026-11-12T18:00:01Z"`), SIM: Scalar(strconv.Quote(testSIM))}
	first, duplicate, err := store.Insert(ctx, in, "SMS Forwarder App")
	if err != nil || duplicate {
		t.Fatalf("insert duplicate=%v err=%v", duplicate, err)
	}
	notificationCtx, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	if notification, err := listener.WaitForNotification(notificationCtx); err != nil || notification.Channel != "sms_messages" {
		t.Fatalf("notification=%+v err=%v", notification, err)
	}
	second, duplicate, err := store.Insert(ctx, in, "SMS Forwarder App")
	if err != nil || !duplicate || second.ID != first.ID {
		t.Fatalf("retry was not idempotent: duplicate=%v err=%v", duplicate, err)
	}
	if string(first.SentStamp) != "1794528000000" || string(first.ReceivedStamp) != `"2026-11-12T18:00:01Z"` {
		t.Fatalf("timestamps changed: %+v", first)
	}
	for index := 0; index < 2; index++ {
		in.Text = fmt.Sprintf("additional %d", index)
		in.SentStamp = nil
		in.ReceivedStamp = nil
		if _, duplicate, err := store.Insert(ctx, in, "SMS Forwarder App"); err != nil || duplicate {
			t.Fatalf("insert err=%v", err)
		}
	}
	page, err := store.List(ctx, Filter{Query: sender, SIM: testSIM, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Messages) != 2 || page.NextCursor == "" {
		t.Fatalf("first page=%+v", page)
	}
	newest, _ := strconv.ParseInt(page.Messages[0].ID, 10, 64)
	next, _ := strconv.ParseInt(page.Messages[1].ID, 10, 64)
	if newest <= next {
		t.Fatalf("messages are not numerically newest first: %+v", page.Messages)
	}
	cursor, _ := parseCursor(page.NextCursor)
	older, err := store.List(ctx, Filter{Query: sender, Before: cursor, Limit: 2})
	if err != nil || len(older.Messages) != 1 || older.Messages[0].ID != first.ID || older.NextCursor != "" {
		t.Fatalf("older=%+v err=%v", older, err)
	}
	page, err = store.List(ctx, Filter{Query: "100%_", SIM: testSIM, Limit: 50})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != first.ID {
		t.Fatalf("literal wildcard search=%+v err=%v", page, err)
	}
	page, err = store.List(ctx, Filter{Query: sender, SIM: "other-sim", Limit: 50})
	if err != nil || page.Total != 0 {
		t.Fatalf("SIM filter page=%+v err=%v", page, err)
	}
	// A separate pool sees the same persisted records.
	otherPool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer otherPool.Close()
	page, err = (Store{Pool: otherPool}).List(ctx, Filter{Query: sender, Limit: 50})
	if err != nil || len(page.Messages) != 3 {
		t.Fatalf("persistence page=%+v err=%v", page, err)
	}
	encoded, _ := json.Marshal(page.Messages[0])
	if len(encoded) == 0 {
		t.Fatal("message could not be serialized")
	}
}

func TestPostgresConversationsAndSenderHistory(t *testing.T) {
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
	prefix := fmt.Sprintf("chat-%d-", time.Now().UnixNano())
	firstSender, secondSender := prefix+"1", prefix+"12"
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM messages WHERE sender = ANY($1)", []string{firstSender, secondSender})
	}()
	insert := func(sender, text, sim string) Message {
		t.Helper()
		m, _, err := store.Insert(ctx, Incoming{From: sender, Text: text, SIM: Scalar(strconv.Quote(sim))}, "test")
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	oldest := insert(firstSender, prefix+" needle 100%_", "SIM 1")
	for index := 1; index < 55; index++ {
		insert(firstSender, fmt.Sprintf("message %d", index), "SIM 1")
	}
	second := insert(secondSender, "another sender", "SIM 1")
	latest := insert(firstSender, "latest on another SIM", "SIM 2")
	page, err := store.Conversations(ctx, Filter{Query: prefix, Limit: 1})
	if err != nil || page.Total != 2 || page.MessageTotal != 57 || len(page.Conversations) != 1 || page.NextCursor == "" {
		t.Fatalf("conversation page=%+v err=%v", page, err)
	}
	if c := page.Conversations[0]; c.LatestMessage.ID != latest.ID || c.MessageCount != 56 {
		t.Fatalf("wrong latest message or incomplete history count: %+v", c)
	}
	cursor, _ := parseCursor(page.NextCursor)
	older, err := store.Conversations(ctx, Filter{Query: prefix, Before: cursor, Limit: 1})
	if err != nil || len(older.Conversations) != 1 || older.Conversations[0].LatestMessage.ID != second.ID || older.NextCursor != "" {
		t.Fatalf("sender repeated across conversation pages: %+v err=%v", older, err)
	}
	page, err = store.Conversations(ctx, Filter{Query: prefix + " needle 100%_", Limit: 50})
	if err != nil || page.Total != 1 || len(page.Conversations) != 1 || page.Conversations[0].LatestMessage.ID != latest.ID || page.Conversations[0].MessageCount != 56 {
		t.Fatalf("search must match older text and retain latest preview: %+v err=%v", page, err)
	}
	page, err = store.Conversations(ctx, Filter{Query: prefix, SIM: "SIM 1", Limit: 50})
	if err != nil || page.Total != 2 || page.MessageTotal != 56 || len(page.Conversations) != 2 || page.Conversations[0].LatestMessage.ID != second.ID || page.Conversations[1].MessageCount != 55 {
		t.Fatalf("SIM-filtered conversations=%+v err=%v", page, err)
	}
	history, err := store.List(ctx, Filter{From: firstSender, Limit: 50})
	if err != nil || history.Total != 56 || len(history.Messages) != 50 || history.Messages[0].ID != latest.ID || history.NextCursor == "" {
		t.Fatalf("sender history=%+v err=%v", history, err)
	}
	for _, message := range history.Messages {
		if message.From != firstSender {
			t.Fatalf("overlapping sender leaked into history: %+v", message)
		}
	}
	cursor, _ = parseCursor(history.NextCursor)
	history, err = store.List(ctx, Filter{From: firstSender, Before: cursor, Limit: 50})
	if err != nil || len(history.Messages) != 6 || history.Messages[5].ID != oldest.ID || history.NextCursor != "" {
		t.Fatalf("older sender history=%+v err=%v", history, err)
	}
	newest := insert(secondSender, "new live message", "SIM 1")
	page, err = store.Conversations(ctx, Filter{Query: prefix, Limit: 50})
	if err != nil || len(page.Conversations) != 2 || page.Conversations[0].LatestMessage.ID != newest.ID || page.Conversations[0].MessageCount != 2 {
		t.Fatalf("new message must move its conversation to the top: %+v err=%v", page, err)
	}
}
