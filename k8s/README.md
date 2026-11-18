# Deploy Relay to k3s

These manifests deploy the application in the **`sms-receiver` namespace** and expose **TCP port `30080`**, forwarding to the application's internal port `8080`. PostgreSQL is managed separately and configured through the `DATABASE_URL` Secret key. This directory contains application resources only.

Prerequisites: an existing k3s cluster, a reachable PostgreSQL database and application login role, a public app image pushed to Docker Hub, and a copy of this repository on the server. Commands below run from the repository root on the k3s server. If you already have a configured `kubectl`, you can use it in place of `sudo k3s kubectl`.

## 1. Set your image

In `k8s/app.yaml`, replace:

```yaml
image: docker.io/your-dockerhub-username/sms-receiver:1.0.0
```

with the exact public image/tag you pushed. The image must match your server's CPU architecture. No registry pull secret is needed for a public image.

## 2. Prepare the namespace and secrets

```sh
sudo k3s kubectl apply -f k8s/namespace.yaml
```

If you have not prepared `.env`, copy the example:

```sh
cp .env.example .env
```

Uncomment and configure `DATABASE_URL`, and replace `VIEWER_PASSWORD` and `FORWARDER_TOKEN` with your own long secrets. For a separately managed PostgreSQL Service named `postgres` in the `databases` namespace, the connection string could be:

```text
DATABASE_URL=postgres://sms_receiver:APP_PASSWORD@postgres.databases.svc:5432/sms_receiver?sslmode=disable
```

Use the actual database host, name, application username, and password. URL-encode special characters in the username/password, or use URL-safe credentials. Use TLS settings appropriate to the database connection; the example assumes a trusted internal cluster network. The role must have permission to create the app's tables, indexes, trigger, and function in its database; the app runs migrations at startup.

Use unquoted `KEY=value` entries for `--from-env-file`. `.env` is ignored by Git. `POSTGRES_PASSWORD`, `POSTGRES_PORT`, and `APP_PORT` in the example file are Docker Compose settings; the Kubernetes app uses `DATABASE_URL` and the port in `app.yaml`.

Create the Kubernetes Secret without putting secret values into these public manifests:

```sh
sudo k3s kubectl -n sms-receiver create secret generic sms-receiver-secrets \
  --from-env-file=.env
```

Run that creation command once. If the Secret already exists from the earlier setup, add its `DATABASE_URL` key using your cluster's secret-management workflow before applying the app manifest. Restart the Deployment after changing its environment secrets. If switching from an existing database, migrate the data before changing the connection URL.

## 3. Start the app

```sh
sudo k3s kubectl apply -f k8s/app.yaml
sudo k3s kubectl -n sms-receiver rollout status deployment/sms-receiver --timeout=180s

sudo k3s kubectl -n sms-receiver get pods,services
```

The configured PostgreSQL server must be available before the app can start. If NetworkPolicies restrict traffic, allow the application to reach its database host and port.

## 4. Open the app

Allow inbound **TCP `30080`** in the server firewall and any cloud firewall/security group for the clients that need access. Then use:

```text
Web inbox:      http://YOUR_SERVER_IP:30080/
SMS Forwarder: http://YOUR_SERVER_IP:30080/messages
```

Sign in with `VIEWER_PASSWORD`. Configure SMS Forwarder to POST JSON with:

```text
Content-Type: application/json
User-Agent: SMS Forwarder App
X-Forwarder-Token: <your FORWARDER_TOKEN>
```

Both the browser and forwarding app must be able to reach the server IP; `localhost` on your phone refers to the phone itself. The SSE stream uses the same port and needs no separate port mapping.

The supplied NodePort serves HTTP for LAN/VPN access. For access over the public internet, put an HTTPS ingress or reverse proxy in front, set `COOKIE_SECURE` to `"true"` in `app.yaml`, and use the HTTPS URL in the browser and forwarding app. Preserve SSE streaming on `/api/events`; avoid proxy response buffering and use an idle timeout above the 20-second heartbeat. Do not expose the PostgreSQL port publicly.

`30080` must be unused by another NodePort Service. To choose another port, change `nodePort` in `app.yaml` to an available port in your cluster's NodePort range (normally `30000–32767`), apply the file again, and update the firewall and URLs.

## Update the app

Push a new immutable version tag, change `image` in `app.yaml`, then run:

```sh
sudo k3s kubectl apply -f k8s/app.yaml
sudo k3s kubectl -n sms-receiver rollout status deployment/sms-receiver --timeout=180s
```

## Diagnose a deployment

```sh
sudo k3s kubectl -n sms-receiver get pods,services
sudo k3s kubectl -n sms-receiver logs deployment/sms-receiver --tail=100
sudo k3s kubectl -n sms-receiver get events --sort-by=.metadata.creationTimestamp
```

For `ImagePullBackOff`, check the image name/tag, registry availability, and server architecture. For `CreateContainerConfigError`, check that the application Secret includes `DATABASE_URL`, `VIEWER_PASSWORD`, and `FORWARDER_TOKEN`. For database connection errors, check the URL, network access, role permissions, and database availability.

References: [Kubernetes namespaces](https://kubernetes.io/docs/concepts/overview/working-with-objects/namespaces/) and [NodePort Services](https://kubernetes.io/docs/concepts/services-networking/service/#type-nodeport).
