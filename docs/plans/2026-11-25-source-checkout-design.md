# Editable LibreDesk source on rep-dev

**Approved decision (item 4):** Give `orca` an editable checkout of
`Allen-LPL/libredesk` at `/opt/libredesk/source`. Keep the running app on the
GHCR image selected by the operator in `/opt/libredesk/.env`. A local edit,
commit, push, or image publication does not hot-reload or deploy the app.

The checkout tracks `origin/main` and uses a repository-scoped writable SSH
deploy key. Only its public half goes to GitHub. Operators edit on branches;
GitHub Actions owns testing and image builds. No source bind mount, build
toolchain, or GitHub CLI is needed on the server.

Keep the deployment directory root-owned. Grant `orca` search permission on
that directory only, without granting directory listing or changing access
to `.env`, config, uploads, or backups. The server account already has sudo
and Docker access; filesystem modes protect ordinary unprivileged use.

Before an explicit rollout, verify the reviewed revision, image digest and
backup. Preserve the old pin and account for database migrations on rollback.
