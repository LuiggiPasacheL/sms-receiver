# Relay — live SMS inbox

A read-only SMS inbox built with **Go**, **React / Next.js (App Router)**, and **PostgreSQL**. SMS Forwarder posts to `/messages`; messages appear in the browser automatically through **Server-Sent Events (SSE)**. Messages cannot be sent, replied to, or edited. The SIM manager can assign aliases and permanently delete a SIM together with all its messages.

For k3s deployment in a dedicated `sms-receiver` namespace on server port **30080**, see [the Kubernetes deployment guide](k8s/README.md).

Messages are grouped by sender phone number (or sender name) into read-only conversations, with the latest preview and message count in the inbox. Opening a conversation shows incoming message bubbles in chronological order, date separators, and older history on demand. The interface includes substring search across senders and message bodies, SIM filtering, cursor pagination, per-message details and copy-to-clipboard, mobile layouts, a live connection indicator, and optional password access. Search finds conversations containing matching messages and opens their full history; the SIM filter applies to both the list and history. Sender values are grouped exactly as forwarded, across SIMs unless a SIM filter is selected. No message data is stored in browser local storage.

## Run with Docker

```sh
cp .env.example .env
# Edit .env: replace POSTGRES_PASSWORD, VIEWER_PASSWORD and FORWARDER_TOKEN.
docker compose up --build -d
```

Open **http://localhost:8080** and sign in with `VIEWER_PASSWORD`.

The root `Dockerfile` is a multi-stage build: it exports the Next.js frontend, compiles the Go server, and packages both in one non-root runtime image. Go serves the frontend and API on the same port, including SSE; no Node.js process is needed at runtime. Compose also starts PostgreSQL, waits for its health check, and persists messages in the `postgres_data` volume. Database migrations run automatically at startup, under a PostgreSQL advisory lock.

```sh
docker compose logs -f app
docker compose down          # keeps the database volume
```

To build just the app image: `docker build -t sms-receiver .`. Run it with the environment variables below and a reachable PostgreSQL database. PostgreSQL 14 or newer is required.

## Configure SMS Forwarder

Set the forwarding method to **HTTP POST** and use:

```text
URL: http://YOUR_SERVER_IP:8080/messages
Content-Type: application/json
User-Agent: SMS Forwarder App
X-Forwarder-Token: <the FORWARDER_TOKEN from .env>
```

The phone must be able to reach this address. On the same Wi-Fi, use your computer/server's LAN IP, not `localhost`. For access over the internet, use your HTTPS domain and set `COOKIE_SECURE=true`.

Map the app's own template variables to these JSON field names:

```json
{
  "from": "+51987654321",
  "text": "Your verification code is 482913.",
  "sentStamp": 1794528000000,
  "receivedStamp": 1794528001000,
  "sim": "SIM 1"
}
```

The example above contains literal test data; substitute the variable syntax provided by your SMS Forwarder app. The endpoint accepts the exact `User-Agent` you supplied, but does not treat it as authentication. Use `X-Forwarder-Token` for authentication. If the forwarding app cannot set custom headers, the standalone Go server allows an empty `FORWARDER_TOKEN`; the provided Compose configuration requires one by default.

`from` and `text` are required non-empty strings. `sentStamp`, `receivedStamp`, and `sim` can be strings, numbers, null, or omitted. Objects/arrays/booleans are rejected for those fields. Timestamp values are preserved exactly in PostgreSQL JSONB. The interface formats Unix seconds/milliseconds and ISO timestamps with explicit timezones, and displays other timestamp strings as supplied. Display times use the browser timezone; the “Received today” count uses UTC and the server's ingestion time.

When at least one timestamp is supplied, retrying the same sender, text, timestamps, and SIM returns the existing message (`200`, `duplicate: true`). A new message returns `201`. Without timestamps, each POST creates a new message because identical texts may be legitimate separate deliveries. Validation failures return `400`; missing/wrong forwarding tokens return `401`; bodies over 128 KiB return `413`.

Try it (replace the token):

```sh
curl -i http://localhost:8080/messages \
  -H 'Content-Type: application/json' \
  -H 'User-Agent: SMS Forwarder App' \
  -H 'X-Forwarder-Token: YOUR_FORWARDER_TOKEN' \
  --data '{"from":"Test phone","text":"Hello from SMS Forwarder!","sentStamp":1794528000000,"receivedStamp":1794528001000,"sim":"SIM 1"}'
```

## Manage SIMs

Open **Manage SIMs** beside the SIM filter. Assign a display alias such as “Personal” or “Work”; leave it empty to restore the original identifier. Aliases are stored in PostgreSQL and appear in filters and message bubbles. The forwarder continues to send its original SIM value, and filtering and deletion always use that exact identifier. Aliases may contain up to 128 characters, and surrounding whitespace is trimmed.

**Delete SIM** opens a confirmation showing the SIM identifier and message count. Confirming permanently deletes every message for that SIM across all conversations and removes its alias in one transaction. Messages from other SIMs remain intact, including those in shared conversations. This does not block future forwarding: a new message with the same SIM identifier makes it appear again without an alias. Messages without a SIM identifier do not appear in the SIM manager.

In Cloudflare Access mode, only administrators can change aliases or delete SIMs. In local password mode, SIM management uses the inbox session. Changes propagate to other open inboxes through SSE, and a deleted SIM filter resets to All SIMs.

## Cloudflare Access and administrators

Set `CLOUDFLARE_ACCESS_TEAM_URL` to `https://YOUR_TEAM.cloudflareaccess.com` and `CLOUDFLARE_ACCESS_AUD` to the Application Audience (AUD) tag of the existing Access application. Set both values in the environment and keep `FORWARDER_TOKEN` configured. For k3s, put these keys in `sms-receiver-secrets` and apply the updated `k8s/app.yaml`. The origin needs HTTPS access to your team domain to retrieve signing keys. See [Cloudflare's JWT validation guide](https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/authorization-cookie/validating-json/).

Users admitted by the Cloudflare Access policy can read the inbox. The server verifies the signature, issuer, audience and expiration of the application JWT. It uses the email from the verified user token to look up administrator privileges. API clients can supply the same kind of JWT via `Authorization: Bearer …` when the Cloudflare assertion header is absent. The shared inbox password is disabled in Cloudflare Access mode.

Bootstrap the first administrator from an operator shell with access to the deployed container. Use the email address that the operator uses to sign in through Cloudflare:

```sh
kubectl -n sms-receiver exec deploy/sms-receiver -- /app/sms-receiver admin add owner@example.com
kubectl -n sms-receiver exec deploy/sms-receiver -- /app/sms-receiver admin list
```

For Compose, use `docker compose exec app /app/sms-receiver admin add owner@example.com`. From the Go checkout in `backend`, run `go run ./cmd/server admin add owner@example.com` with `DATABASE_URL` set. Repeating the same address is safe. The administrator list is stored in PostgreSQL and shared by all app replicas.

After signing in through Cloudflare, open **Administrators** in the top bar to add more email addresses. Added users must also be allowed by your Cloudflare Access policy. Adding an address in Relay grants app permissions only. Reload the inbox after a role is granted to reveal the administrator controls. Administrators can add emails and use **Manage SIMs** to edit aliases or delete SIMs and their stored messages.

Keep `/messages` reachable by SMS Forwarder using its current Cloudflare routing or service policy and `X-Forwarder-Token`. The browser paths, including `/api/*`, should share the Access application and AUD. Turn off origin caching for authenticated API responses.

## Live updates

`GET /api/events` maintains an authenticated SSE connection. PostgreSQL triggers emit `NOTIFY` for message inserts/deletions and SIM alias changes, and each API instance uses `LISTEN` on a dedicated connection to broadcast an invalidation to its connected browsers. The browser then reloads its current message window and filter counts. There is no browser polling loop.

Every SSE connection begins with a `ready` event and a fresh inbox fetch. If a browser or database listener reconnects, clients refresh again to recover missed changes. Heartbeat comments are sent every 20 seconds, writes have deadlines, and notifications are coalesced for slow clients. The server cancels streams on shutdown.

If you put Nginx or another proxy in front of the app, disable buffering and caching for `/api/events`, preserve streaming, and allow an idle timeout above 20 seconds. Example:

```nginx
location /api/events {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_buffering off;
    proxy_cache off;
    proxy_read_timeout 60s;
}
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
}
```

## Local development

Requirements: Node.js 22+, Go 1.26+, and PostgreSQL 14+. The Dockerfile uses Node 22 and Go 1.27.

Start just PostgreSQL with `docker compose up -d db` after preparing `.env`. In one terminal, from the repository root:

```sh
set -a
. ./.env
set +a
export DATABASE_URL="postgres://sms:${POSTGRES_PASSWORD}@localhost:${POSTGRES_PORT:-5432}/sms?sslmode=disable"
cd backend
go run ./cmd/server
```

In another terminal:

```sh
cd frontend
npm ci
npm run dev
```

Open **http://localhost:3000**. During development, Next.js proxies `/api/*` and `/messages` to Go at `http://127.0.0.1:8080`; set `API_PROXY_TARGET` to change it. Keep `COOKIE_SECURE=false` for local HTTP. A production frontend export is served by Go from `frontend/out`.

## Configuration

| Variable | Meaning | Default for standalone Go |
| --- | --- | --- |
| `DATABASE_URL` | PostgreSQL connection string; use TLS for remote databases | Required |
| `HTTP_ADDR` | Go listen address | `:8080` |
| `STATIC_DIR` | Exported frontend directory | `../frontend/out` (run from `backend`) |
| `VIEWER_PASSWORD` | Password for legacy inbox access without Cloudflare | Empty: public inbox when Cloudflare Access is disabled |
| `CLOUDFLARE_ACCESS_TEAM_URL` | HTTPS URL for the Cloudflare Access team | Empty |
| `CLOUDFLARE_ACCESS_AUD` | Audience tag for the Cloudflare Access app | Empty |
| `FORWARDER_TOKEN` | Required `X-Forwarder-Token` header on ingestion | Empty: unauthenticated ingestion |
| `COOKIE_SECURE` | Use Secure session cookies for HTTPS | `false` |
| `APP_PORT` | Compose's exposed app port | `8080` |
| `POSTGRES_PASSWORD` | Compose database password; use URL-safe characters | Required by Compose |
| `POSTGRES_PORT` | Compose database port bound to localhost | `5432` |

Compose requires all three secrets. The frontend assets are public; all inbox data and the event stream require a valid session when `VIEWER_PASSWORD` is set. Changing that password invalidates existing sessions. Deploy behind HTTPS when exposing the app outside your LAN. The application does not log message bodies or forwarding credentials. Do not commit `.env`.

## API

| Route | Purpose |
| --- | --- |
| `POST /messages` | SMS Forwarder webhook; optional token authentication |
| `GET /api/conversations?q=&sim=&limit=50&before=` | Authenticated conversations, newest activity first; includes `conversations` (`latestMessage`, `messageCount`), `nextCursor`, conversation `total`, `messageTotal`, `today`, and known `sims` |
| `GET /api/messages?q=&sim=&from=&limit=50&before=` | Authenticated message history; `from` filters by exact sender; newest first, `limit` 1–100; response includes `messages`, `nextCursor`, filtered `total`/`today`, and all known `sims` |
| `GET /api/sims` | SIM inventory: `sims` entries contain original `id`, display `alias`, and `messageCount` |
| `PUT /api/sims` | Set/clear alias with `{"sim":"SIM 1","alias":"Personal"}`; `204` on success, `404` for an unknown SIM |
| `DELETE /api/sims?sim=` | Permanently remove the exact SIM and all its messages; returns `deletedMessages`, or `404` if unknown |
| `GET /api/events` | Authenticated SSE; `ready` and `messages` events |
| `GET /api/session` | Session status, including the verified email and administrator status in Cloudflare Access mode |
| `GET /api/admins` | List administrator emails, available to Cloudflare administrators |
| `PUT /api/admins` | Grant administrator access with `{"email":"owner@example.com"}` |
| `POST /api/session` | Sign in with `{"password":"..."}` |
| `DELETE /api/session` | Sign out |
| `GET /healthz` | Database readiness check |

Message IDs and pagination cursors are strings to preserve PostgreSQL BIGINT precision in JavaScript.

## Checks

```sh
cd backend
go test -race ./...
go vet ./...
cd ../frontend
npm run typecheck
npm run build
```

The PostgreSQL integration test uses a separate, empty test database and is skipped unless `TEST_DATABASE_URL` is set. It creates the schema and inserts uniquely tagged fixtures; it cleans up only those fixtures.

```sh
cd backend
TEST_DATABASE_URL='postgres://sms:password@localhost:5432/sms_test?sslmode=disable' go test -race ./...
```

Framework documentation: [Next.js App Router](https://nextjs.org/docs/app/getting-started) and [pgx PostgreSQL driver](https://pkg.go.dev/github.com/jackc/pgx/v5).
