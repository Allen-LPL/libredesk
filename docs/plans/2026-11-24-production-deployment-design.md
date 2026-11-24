# LibreDesk Production Deployment Design

## Goal

Run one LibreDesk instance for `mydesign-fashion.com` and `liupengliang.com`.
The canonical customer-support URL is `https://support.liupengliang.com`.
Each project will use a separate inbox, team, and widget inside the shared
LibreDesk instance.

## Architecture

```text
Customers and agents
        |
        | HTTPS
        v
support.liupengliang.com
        |
        v
ys-api-001 Nginx
        |
        | Tailscale
        v
rep-dev 100.64.0.27:9000
  |- LibreDesk
  |- PostgreSQL
  |- Redis
  `- persistent uploads
```

`ys-api-001` terminates TLS and forwards requests to `rep-dev` over
Tailscale. Only the LibreDesk application port is reachable over Tailscale.
PostgreSQL and Redis remain on a private Docker network and publish no host
ports.

## Build and release

GitHub Actions builds the `Allen-LPL/libredesk` fork and publishes an amd64
image to GitHub Container Registry. Every image has an immutable commit SHA
tag. The workflow also maintains a `main` tag for convenience, but production
deployment pins the immutable tag.

The build runs the existing Go and frontend tests before publishing. The image
records the source repository and commit as OCI labels. Production does not
build from source on `rep-dev`.

## Runtime

The production stack lives under `/opt/libredesk` on `rep-dev`:

- `compose.yml`: application, PostgreSQL, and Redis services.
- `.env`: generated database and initial administrator secrets, readable only
  by root.
- `config.toml`: production configuration with a persistent 32-character
  encryption key.
- `uploads/`: persistent uploaded files.
- Docker volumes: PostgreSQL and Redis data.

The application binds to `100.64.0.27:9000`. Containers have health checks and
restart unless stopped. The application starts with idempotent install and
upgrade commands so normal restarts do not erase existing data.

## Reverse proxy and TLS

`ys-api-001` gets a dedicated
`/etc/nginx/conf.d/support.liupengliang.com.conf`. HTTP redirects to HTTPS
except for the ACME challenge. HTTPS proxies to
`http://100.64.0.27:9000` and sets:

- `Host`
- `X-Client-IP`
- `X-Real-IP`
- `X-Forwarded-For`
- `X-Forwarded-Proto`
- WebSocket upgrade headers

The proxy permits request bodies up to 100 MB and uses extended read timeouts
for long-lived requests. Existing Nginx configuration is backed up before any
change. A certificate is issued only after public DNS resolves to the
`ys-api-001` ingress.

## Security

- No PostgreSQL or Redis host ports are published.
- Port 9000 is bound only to the Tailscale address.
- Secrets are generated on `rep-dev`, never committed, and never printed in
  reports.
- The LibreDesk encryption key is backed up and must not change after initial
  deployment.
- Nginx overwrites `X-Client-IP`; direct public access to the app port is
  impossible.
- GHCR access uses the smallest practical scope. A read-only token is needed
  on `rep-dev` only if the package is private.

## Backup and recovery

A root-owned backup script creates:

- a compressed PostgreSQL dump;
- an archive of uploads and production configuration;
- a manifest containing the deployed image tag.

Backups are retained locally for seven days initially. Off-host backup is a
follow-up because no destination has been selected. Before an application
upgrade, the database and uploads are backed up.

Rollback pins the previous image SHA and restarts only the application.
Database migrations may not be reversible, so restore from the pre-upgrade
database dump when required.

## Validation

Validation is layered:

1. GitHub Actions tests and image publication succeed.
2. PostgreSQL and Redis health checks pass.
3. LibreDesk responds locally inside its Docker network.
4. `ys-api-001` reaches `100.64.0.27:9000`.
5. `nginx -t` passes before reload.
6. HTTPS certificate and redirect behavior are correct.
7. Login, upload, WebSocket/live-chat, and real client IP behavior work.
8. `hk` and `ms` can access the public URL.
9. A database dump is created and can be inspected.

## Failure handling

Existing services remain unchanged until the new stack is healthy. Nginx is
reloaded only after syntax validation. If proxy validation fails, restore the
timestamped Nginx backup. If the application fails, keep Nginx disabled for
the new hostname, inspect container health and logs, and roll back to the
previous immutable image.

## Current blocker

At design time, public DNS resolvers return no record for
`support.liupengliang.com`. Application deployment can proceed, but
certificate issuance and public end-to-end validation must wait until the DNS
record resolves to the `ys-api-001` ingress.
