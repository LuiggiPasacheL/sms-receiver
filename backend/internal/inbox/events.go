package inbox

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Events coalesces invalidations. The database remains the source of truth, so a
// slow client needs one refresh rather than an unbounded queue of message bodies.
type Events struct {
	mu      sync.Mutex
	clients map[chan struct{}]struct{}
}

func NewEvents() *Events { return &Events{clients: make(map[chan struct{}]struct{})} }

func (e *Events) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	e.mu.Lock()
	e.clients[ch] = struct{}{}
	e.mu.Unlock()
	return ch, func() {
		e.mu.Lock()
		delete(e.clients, ch)
		e.mu.Unlock()
	}
}

func (e *Events) Publish() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for ch := range e.clients {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// LISTEN/NOTIFY works across API replicas. On listener recovery every client
// refreshes, covering messages committed while the connection was unavailable.
func (e *Events) Listen(ctx context.Context, databaseURL string, logger *slog.Logger) {
	for ctx.Err() == nil {
		err := e.listenOnce(ctx, databaseURL)
		if ctx.Err() != nil {
			return
		}
		logger.Warn("database event listener reconnecting", "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (e *Events) listenOnce(ctx context.Context, databaseURL string) error {
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, err := pgx.Connect(connectCtx, databaseURL)
	cancel()
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = conn.Close(closeCtx)
	}()
	if _, err = conn.Exec(ctx, "LISTEN sms_messages"); err != nil {
		return err
	}
	e.Publish()
	for {
		if _, err = conn.WaitForNotification(ctx); err != nil {
			return err
		}
		e.Publish()
	}
}
