CREATE TABLE IF NOT EXISTS messages (
    id BIGSERIAL PRIMARY KEY,
    sender TEXT NOT NULL,
    body TEXT NOT NULL,
    sent_stamp JSONB NOT NULL DEFAULT 'null',
    received_stamp JSONB NOT NULL DEFAULT 'null',
    sim TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    fingerprint BYTEA UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS messages_sim_id_idx ON messages (sim, id DESC);
CREATE INDEX IF NOT EXISTS messages_sender_id_idx ON messages (sender, id DESC);
CREATE INDEX IF NOT EXISTS messages_created_at_idx ON messages (created_at);

CREATE OR REPLACE FUNCTION notify_sms_message() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('sms_messages', '');
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE TRIGGER messages_notify
AFTER INSERT OR DELETE ON messages FOR EACH STATEMENT EXECUTE FUNCTION notify_sms_message();
