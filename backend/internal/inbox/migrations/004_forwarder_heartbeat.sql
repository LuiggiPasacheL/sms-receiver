-- One monitored forwarding phone per inbox. Keep only its most recent ping.
CREATE TABLE IF NOT EXISTS forwarder_heartbeat (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    last_seen TIMESTAMPTZ NOT NULL
);

CREATE OR REPLACE TRIGGER forwarder_heartbeat_notify
AFTER INSERT OR UPDATE ON forwarder_heartbeat
FOR EACH STATEMENT EXECUTE FUNCTION notify_sms_message();
