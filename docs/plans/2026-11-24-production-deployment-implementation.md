# LibreDesk Production Deployment Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build the Allen-LPL LibreDesk fork in GitHub Actions, run it on `rep-dev`, and expose it through `ys-api-001` at `https://support.liupengliang.com`.

**Architecture:** GitHub Actions tests and publishes an amd64 image to GHCR with an immutable commit tag. `rep-dev` runs LibreDesk, PostgreSQL, and Redis in an isolated Compose project; only the app binds to the Tailscale address. `ys-api-001` terminates TLS and proxies to the app over Tailscale.

**Tech Stack:** GitHub Actions, Go 1.27.3, Node.js 24, pnpm, Docker Buildx, GHCR, Docker Compose, PostgreSQL 17, Redis 7, Tailscale, Nginx 1.24, Certbot.

---

### Task 1: Add the production image workflow

**Files:**
- Create: `.github/workflows/production-image.yml`
- Create: `scripts/verify-production-image-workflow.sh`

**Step 1: Write the failing workflow test**

Create a shell test that parses the workflow as YAML and asserts:

- pushes to `main` trigger it;
- packages have write permission;
- Go tests and frontend tests run before image publication;
- the image has both `sha-<commit>` and `main` tags;
- the repository owner determines the GHCR namespace.

Use Ruby's standard YAML parser so the test does not require a new project
dependency.

**Step 2: Run the test to verify it fails**

Run:

```bash
bash scripts/verify-production-image-workflow.sh
```

Expected: FAIL because `.github/workflows/production-image.yml` does not exist.

**Step 3: Add the workflow**

Create `.github/workflows/production-image.yml` with:

- `push` on `main` and manual `workflow_dispatch`;
- `contents: read` and `packages: write`;
- checkout, Go 1.27.3, Node 24, and pnpm setup;
- `go test -count=1 ./...`;
- `pnpm install --frozen-lockfile` and `pnpm test:run`;
- `make build`;
- GHCR login using `GITHUB_TOKEN`;
- Buildx publication using the existing runtime `Dockerfile`;
- `ghcr.io/${owner}/libredesk:sha-${GITHUB_SHA}` and
  `ghcr.io/${owner}/libredesk:main` tags;
- OCI source, revision, and license labels.

Build only `linux/amd64`, matching `rep-dev`.

**Step 4: Run the test**

Run:

```bash
bash scripts/verify-production-image-workflow.sh
```

Expected: PASS.

**Step 5: Commit**

```bash
git add .github/workflows/production-image.yml scripts/verify-production-image-workflow.sh
git commit -m "ci: publish production image to GHCR"
```

### Task 2: Add production runtime manifests

**Files:**
- Create: `deploy/production/compose.yml`
- Create: `deploy/production/config.toml`
- Create: `deploy/production/.env.example`
- Create: `scripts/verify-production-compose.sh`

**Step 1: Write the failing Compose security test**

The test must render Compose with dummy secrets and assert:

- the application image is supplied by `LIBREDESK_IMAGE`;
- the app publishes only `100.64.0.27:9000:9000`;
- PostgreSQL and Redis publish no host ports;
- all three services have health checks and restart policies;
- secrets have no insecure defaults;
- uploads, PostgreSQL, and Redis are persistent;
- the production config trusts only `X-Client-IP`;
- secure cookies remain enabled;
- the upload path is persistent.

**Step 2: Run the test to verify it fails**

Run:

```bash
bash scripts/verify-production-compose.sh
```

Expected: FAIL because the production manifests do not exist.

**Step 3: Add the runtime files**

Create `compose.yml` with:

- `app`, `db`, and `redis` services;
- PostgreSQL 17 Alpine and Redis 7 Alpine;
- passwords supplied only through `.env`;
- Redis authentication enabled;
- an app command that runs idempotent install, upgrade, then the server;
- an app port mapping bound to `100.64.0.27`;
- internal-only database and Redis networking;
- named volumes for PostgreSQL and Redis;
- a bind mount for `uploads`;
- health checks and `restart: unless-stopped`.

Create `config.toml` with production logging, secure cookies,
`client_ip_header = "X-Client-IP"`, Docker service names, and
`/libredesk/uploads`.

Create `.env.example` containing placeholders only. Do not commit real
passwords or the encryption key.

**Step 4: Run the Compose security test**

Run:

```bash
bash scripts/verify-production-compose.sh
```

Expected: PASS.

**Step 5: Commit**

```bash
git add deploy/production scripts/verify-production-compose.sh
git commit -m "ops: add hardened production compose stack"
```

### Task 3: Add Nginx and backup assets

**Files:**
- Create: `deploy/nginx/support.liupengliang.com.http.conf`
- Create: `deploy/nginx/support.liupengliang.com.conf`
- Create: `deploy/production/backup.sh`
- Create: `scripts/verify-production-ops.sh`

**Step 1: Write the failing operations test**

Assert that:

- the HTTP bootstrap config serves the ACME challenge;
- the final config redirects HTTP to HTTPS;
- the final config proxies to `100.64.0.27:9000`;
- `X-Client-IP` is overwritten with `$remote_addr`;
- WebSocket headers are set;
- request size and proxy timeouts are explicit;
- the backup script runs `pg_dump`, archives uploads and config, writes an
  image manifest, uses restrictive permissions, and removes backups older than
  seven days.

**Step 2: Run it and verify failure**

Run:

```bash
bash scripts/verify-production-ops.sh
```

Expected: FAIL because the files do not exist.

**Step 3: Add minimal operational assets**

Implement both Nginx stages and a root-run backup script. The script reads
Compose secrets from `/opt/libredesk/.env` without printing them. It writes to
`/opt/libredesk/backups` and uses a timestamped temporary directory before
creating the final archive.

**Step 4: Run the test**

Run:

```bash
bash scripts/verify-production-ops.sh
```

Expected: PASS.

**Step 5: Commit**

```bash
git add deploy/nginx deploy/production/backup.sh scripts/verify-production-ops.sh
git commit -m "ops: add nginx and backup assets"
```

### Task 4: Validate and publish the repository changes

**Files:**
- Verify all files from Tasks 1–3.

**Step 1: Run repository-local static checks**

Run:

```bash
bash scripts/verify-production-image-workflow.sh
bash scripts/verify-production-compose.sh
bash scripts/verify-production-ops.sh
git diff --check origin/main...HEAD
```

Expected: all checks pass with no whitespace errors.

Do not install Node or deploy a development environment on the local Mac.

**Step 2: Push the deployment branch**

```bash
git push -u origin deploy/production
```

Expected: branch is published successfully.

**Step 3: Merge to `main`**

Merge only after reviewing the exact diff. Push `main` to trigger the image
workflow.

**Step 4: Verify GitHub Actions**

Use GitHub CLI to confirm:

- Go tests pass;
- frontend tests pass;
- the image build and push pass;
- the published image has an immutable SHA tag.

Expected: a successful workflow run and a pullable
`ghcr.io/allen-lpl/libredesk:sha-<full-commit>`.

### Task 5: Prepare `rep-dev`

**Files on `rep-dev`:**
- Create: `/opt/libredesk/compose.yml`
- Create: `/opt/libredesk/config.toml`
- Create: `/opt/libredesk/.env`
- Create: `/opt/libredesk/backup.sh`
- Create directories: `/opt/libredesk/uploads`, `/opt/libredesk/backups`

**Step 1: Record the pre-deployment state**

Capture:

```bash
hostname
docker ps --format '{{.Names}}|{{.Image}}|{{.Ports}}'
ss -lnt
df -h /
free -h
tailscale status --self=true --peers=false
```

Expected: port 9000 is unused and Tailscale address is `100.64.0.27`.

**Step 2: Create directories with restrictive permissions**

Create `/opt/libredesk` as root with mode `0750`, `.env` with mode `0600`,
uploads with mode `0750`, and backups with mode `0700`.

**Step 3: Generate secrets without printing them**

Generate:

- a 32-character LibreDesk encryption key;
- a PostgreSQL password;
- a Redis password;
- an initial System user password.

Write them directly to `.env`. Do not send them to chat or shell output.

**Step 4: Install reviewed deployment assets**

Copy the Compose file, config, and backup script from the committed repository.
Set `LIBREDESK_IMAGE` to the immutable GHCR SHA tag.

If GHCR is private, authenticate Docker with a read-only package token through
standard input. Do not place the token in shell history or the repository.

**Step 5: Validate without starting**

Run:

```bash
cd /opt/libredesk
docker compose config --quiet
docker compose pull
```

Expected: valid configuration and all images pulled.

### Task 6: Start and validate LibreDesk privately

**Step 1: Start database and Redis**

```bash
docker compose up -d db redis
docker compose ps
```

Expected: both services become healthy.

**Step 2: Start LibreDesk**

```bash
docker compose up -d app
docker compose ps
```

Expected: the app becomes healthy without a restart loop.

**Step 3: Inspect bounded logs**

```bash
docker compose logs --tail=100 app
```

Expected: successful idempotent install/upgrade and an active HTTP listener.
Do not print environment variables.

**Step 4: Validate Tailscale access**

From `ys-api-001`:

```bash
curl -fsS -o /dev/null -w '%{http_code}\n' \
  -H 'Host: support.liupengliang.com' \
  -H 'X-Client-IP: 127.0.0.1' \
  http://100.64.0.27:9000/
```

Expected: an application HTTP response rather than connection failure.

### Task 7: Configure the HTTP ingress and issue TLS

**Files on `ys-api-001`:**
- Create: `/etc/nginx/conf.d/support.liupengliang.com.conf`
- Backup: timestamped copy under `/etc/nginx/backups/`

**Step 1: Verify DNS**

Query multiple public resolvers. Confirm the A/AAAA record resolves to the
actual `ys-api-001` ingress. Do not issue a certificate before this passes.

**Step 2: Back up Nginx**

Create a timestamped archive of `/etc/nginx/nginx.conf`,
`/etc/nginx/conf.d`, and the certificate metadata needed for rollback.

**Step 3: Install the HTTP bootstrap config**

Install the reviewed HTTP-only config, then run:

```bash
nginx -t
```

Expected: syntax is successful.

**Step 4: Reload and test the ACME path**

Reload Nginx and verify the challenge directory is publicly reachable.

**Step 5: Issue the certificate**

Use Certbot webroot mode for `support.liupengliang.com`. Do not alter unrelated
virtual hosts.

Expected: certificate and key appear under
`/etc/letsencrypt/live/support.liupengliang.com/`.

**Step 6: Install the final HTTPS config**

Replace the bootstrap config with the reviewed final config. Run `nginx -t`
before reload.

Expected: HTTPS proxies successfully to LibreDesk.

### Task 8: End-to-end validation and backup

**Step 1: Validate public HTTP behavior**

Verify:

- HTTP redirects to HTTPS;
- the certificate hostname and chain are valid;
- `/` loads without a proxy error;
- response headers do not expose private services.

**Step 2: Validate from `hk` and `ms`**

Run HTTPS probes from both machines. Record DNS resolution, TCP reachability,
TLS verification, HTTP status, and total request time.

Expected: both clients receive a valid LibreDesk response over HTTPS.

**Step 3: Validate application behavior**

Using the System account:

- log in;
- change the initial password if required;
- create separate inboxes for `mydesign-fashion.com` and
  `liupengliang.com`;
- verify a small upload;
- load each widget;
- verify a live message reaches the shared inbox.

Never print or record the System password.

**Step 4: Run the first backup**

```bash
/opt/libredesk/backup.sh
```

Expected: a new archive and manifest exist with restrictive permissions, and
the PostgreSQL dump can be listed from the archive.

**Step 5: Record the deployed state**

Record only non-secret evidence:

- Git commit and image digest;
- container health;
- Nginx config test;
- certificate expiry;
- `hk` and `ms` probe results;
- backup filename and size;
- any skipped application-level checks.

### Task 9: Schedule backups and document rollback

**Files on `rep-dev`:**
- Create: `/etc/cron.d/libredesk-backup`

**Step 1: Install the schedule**

Run the backup daily at a low-traffic hour with output sent to syslog or a
root-owned log. Avoid overlapping runs with `flock`.

**Step 2: Test the scheduled command manually**

Run exactly the cron command once.

Expected: one valid backup is produced and stale-file cleanup is harmless.

**Step 3: Document rollback commands**

Document how to:

- pin the previous immutable image;
- restart only the app;
- restore PostgreSQL from a selected dump;
- restore uploads;
- restore the Nginx backup.

Do not perform a destructive restore during production validation.

**Step 4: Final verification**

Run:

```bash
docker compose ps
nginx -t
curl -fsSI https://support.liupengliang.com/
```

Expected: healthy containers, valid Nginx configuration, and valid HTTPS.
