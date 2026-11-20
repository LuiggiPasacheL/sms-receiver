package inbox

import (
	"context"
	_ "embed"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/001_messages.sql
var schema string

//go:embed migrations/002_sim_aliases.sql
var simSchema string

var ErrSIMNotFound = errors.New("SIM not found")

type Repository interface {
	Insert(context.Context, Incoming, string) (Message, bool, error)
	List(context.Context, Filter) (Page, error)
	Conversations(context.Context, Filter) (ConversationPage, error)
	SIMs(context.Context) ([]SIM, error)
	SetSIMAlias(context.Context, string, string) error
	DeleteSIM(context.Context, string) (int64, error)
	Ping(context.Context) error
}

type Store struct{ Pool *pgxpool.Pool }

func (s Store) Migrate(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(824536912)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, schema); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, simSchema); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const columns = "id::text, sender, body, sent_stamp, received_stamp, sim, created_at"

func scanMessage(row pgx.Row) (Message, error) {
	var m Message
	err := row.Scan(&m.ID, &m.From, &m.Text, &m.SentStamp, &m.ReceivedStamp, &m.SIM, &m.CreatedAt)
	return m, err
}

func (s Store) Insert(ctx context.Context, in Incoming, agent string) (Message, bool, error) {
	fingerprint := in.fingerprint()
	m, err := scanMessage(s.Pool.QueryRow(ctx,
		`INSERT INTO messages (sender, body, sent_stamp, received_stamp, sim, user_agent, fingerprint)
		VALUES ($1, $2, $3::jsonb, $4::jsonb, $5, $6, $7)
		ON CONFLICT (fingerprint) DO NOTHING RETURNING `+columns,
		in.From, in.Text, jsonValue(in.SentStamp), jsonValue(in.ReceivedStamp), in.SIM.Text(), agent, fingerprint))
	if errors.Is(err, pgx.ErrNoRows) {
		m, err = scanMessage(s.Pool.QueryRow(ctx, "SELECT "+columns+" FROM messages WHERE fingerprint = $1", fingerprint))
		return m, true, err
	}
	return m, false, err
}

func (s Store) List(ctx context.Context, f Filter) (Page, error) {
	p := Page{Messages: []Message{}, SIMs: []string{}}
	query := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(f.Query)
	pattern := "%" + query + "%"
	const where = ` WHERE ($1 = '' OR sender ILIKE $2 OR body ILIKE $2) AND ($3 = '' OR sim = $3) AND ($4 = '' OR sender = $4)`
	if err := s.Pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE created_at >= date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC') FROM messages`+where,
		f.Query, pattern, f.SIM, f.From).Scan(&p.Total, &p.Today); err != nil {
		return p, err
	}
	rows, err := s.Pool.Query(ctx, "SELECT "+columns+" FROM messages"+where+` AND ($5::bigint = 0 OR id < $5) ORDER BY messages.id DESC LIMIT $6`, f.Query, pattern, f.SIM, f.From, f.Before, f.Limit+1)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		m, scanErr := scanMessage(rows)
		if scanErr != nil {
			rows.Close()
			return p, scanErr
		}
		p.Messages = append(p.Messages, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	if len(p.Messages) > f.Limit {
		p.Messages = p.Messages[:f.Limit]
		p.NextCursor = p.Messages[len(p.Messages)-1].ID
	}
	p.SIMs, err = s.knownSIMs(ctx)
	return p, err
}

// Group before applying the cursor, so a sender appears only once even when its
// history spans many message pages. Search matches the entire conversation;
// the preview and count still include every message on the selected SIM.
func (s Store) Conversations(ctx context.Context, f Filter) (ConversationPage, error) {
	p := ConversationPage{Conversations: []Conversation{}, SIMs: []string{}}
	query := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(f.Query)
	const grouped = `WITH grouped AS (
		SELECT sender, max(id) AS latest_id, count(*) AS message_count,
		count(*) FILTER (WHERE created_at >= date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC') AS today_count
		FROM messages WHERE ($3 = '' OR sim = $3)
		GROUP BY sender HAVING ($1 = '' OR sender ILIKE $2 OR bool_or(body ILIKE $2))
	) `
	if err := s.Pool.QueryRow(ctx, grouped+`SELECT count(*), coalesce(sum(message_count), 0), coalesce(sum(today_count), 0) FROM grouped`,
		f.Query, "%"+query+"%", f.SIM).Scan(&p.Total, &p.MessageTotal, &p.Today); err != nil {
		return p, err
	}
	rows, err := s.Pool.Query(ctx, grouped+`SELECT messages.id::text, messages.sender, messages.body,
		messages.sent_stamp, messages.received_stamp, messages.sim, messages.created_at, grouped.message_count
		FROM grouped JOIN messages ON messages.id = grouped.latest_id
		WHERE ($4::bigint = 0 OR messages.id < $4) ORDER BY messages.id DESC LIMIT $5`,
		f.Query, "%"+query+"%", f.SIM, f.Before, f.Limit+1)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var c Conversation
		m := &c.LatestMessage
		if err := rows.Scan(&m.ID, &m.From, &m.Text, &m.SentStamp, &m.ReceivedStamp, &m.SIM, &m.CreatedAt, &c.MessageCount); err != nil {
			rows.Close()
			return p, err
		}
		p.Conversations = append(p.Conversations, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	if len(p.Conversations) > f.Limit {
		p.Conversations = p.Conversations[:f.Limit]
		p.NextCursor = p.Conversations[len(p.Conversations)-1].LatestMessage.ID
	}
	p.SIMs, err = s.knownSIMs(ctx)
	return p, err
}

func (s Store) knownSIMs(ctx context.Context) ([]string, error) {
	values := []string{}
	sims, err := s.Pool.Query(ctx, "SELECT DISTINCT sim FROM messages WHERE sim <> '' ORDER BY sim")
	if err != nil {
		return values, err
	}
	defer sims.Close()
	for sims.Next() {
		var sim string
		if err := sims.Scan(&sim); err != nil {
			return values, err
		}
		values = append(values, sim)
	}
	return values, sims.Err()
}

func (s Store) SIMs(ctx context.Context) ([]SIM, error) {
	values := []SIM{}
	rows, err := s.Pool.Query(ctx, `WITH counts AS (
		SELECT sim, count(*) AS message_count FROM messages WHERE sim <> '' GROUP BY sim
	) SELECT coalesce(counts.sim, sim_aliases.sim), coalesce(sim_aliases.alias, ''), coalesce(counts.message_count, 0)
		FROM counts FULL JOIN sim_aliases ON counts.sim = sim_aliases.sim
		ORDER BY coalesce(counts.sim, sim_aliases.sim)`)
	if err != nil {
		return values, err
	}
	defer rows.Close()
	for rows.Next() {
		var sim SIM
		if err := rows.Scan(&sim.ID, &sim.Alias, &sim.MessageCount); err != nil {
			return values, err
		}
		values = append(values, sim)
	}
	return values, rows.Err()
}

func (s Store) SetSIMAlias(ctx context.Context, id, alias string) error {
	result, err := s.Pool.Exec(ctx, `INSERT INTO sim_aliases (sim, alias)
		SELECT $1, $2 WHERE EXISTS (SELECT 1 FROM messages WHERE sim = $1)
			OR EXISTS (SELECT 1 FROM sim_aliases WHERE sim = $1)
		ON CONFLICT (sim) DO UPDATE SET alias = EXCLUDED.alias`, id, alias)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrSIMNotFound
	}
	return nil
}

func (s Store) DeleteSIM(ctx context.Context, id string) (int64, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, "DELETE FROM messages WHERE sim = $1", id)
	if err != nil {
		return 0, err
	}
	alias, err := tx.Exec(ctx, "DELETE FROM sim_aliases WHERE sim = $1", id)
	if err != nil {
		return 0, err
	}
	if result.RowsAffected() == 0 && alias.RowsAffected() == 0 {
		return 0, ErrSIMNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

func (s Store) Ping(ctx context.Context) error { return s.Pool.Ping(ctx) }

func validLimit(value string) (int, error) {
	if value == "" {
		return 50, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > 100 {
		return 0, errors.New("limit must be between 1 and 100")
	}
	return n, nil
}
