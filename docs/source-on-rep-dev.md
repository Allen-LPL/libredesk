# Editing LibreDesk on rep-dev

`/opt/libredesk/source` is a Git working copy. **Editing it does not hot-reload
production.** Production uses `/opt/libredesk/compose.yml`, with an explicit
`LIBREDESK_IMAGE` pin in the root-owned `/opt/libredesk/.env`.

## Make and review a change

Log in with `ssh rep-dev` as `orca`. Use Git without sudo:

```sh
cd /opt/libredesk/source
git status --short --branch
# Continue only with a clean tree; preserve any other operator's work.
git switch main
git pull --ff-only
git switch -c fix/short-description
# Edit the files, then inspect and stage only the intended changes.
git diff
git add -- path/to/changed-file
git diff --cached
git commit -m "Describe the change"
git push -u origin HEAD
```

If Git asks for an identity, set your own `user.name` and `user.email` with
`git config --local` before committing. Open a PR to `main` from the GitHub
web UI or an authenticated workstation. Obtain review and check the PR
results before merging. Never copy runtime secrets into this checkout.

## Validate the published image

After merge, wait for [Production image](https://github.com/Allen-LPL/libredesk/actions/workflows/production-image.yml)
to pass for the **full main commit SHA**. The workflow runs Go and frontend
tests and publishes `ghcr.io/allen-lpl/libredesk:sha-<full-sha>`. Publication
does not change the deployed image.

On rep-dev, set `sha` to the reviewed main SHA and pull the candidate without
recreating any service:

```sh
image="ghcr.io/allen-lpl/libredesk:sha-$sha"
sudo docker pull "$image"
sudo docker image inspect --format '{{.Os}}/{{.Architecture}} {{index .Config.Labels "org.opencontainers.image.revision"}} {{json .RepoDigests}}' "$image"
```

Require `linux/amd64`, the exact revision, and the registry digest from the
successful publish run. Record the matching `ghcr.io/allen-lpl/libredesk@sha256:…`
reference; use that digest for promotion rather than a moving tag. Validate
application changes and migrations against isolated test data before approval.

## Explicit rollout and rollback

1. Schedule the change. Record the current reference with
   `sudo docker inspect --format '{{.Config.Image}} {{.Image}}' libredesk-app`.
   Run `sudo /opt/libredesk/backup.sh` and verify the reported archive exists
   and contains `database.dump`, uploads, runtime configuration and image
   manifest (`sudo tar -tzf <archive>`). Keep the archive private.
2. **After rollout approval**, use `sudoedit /opt/libredesk/.env` to update
   only `LIBREDESK_IMAGE` to the verified digest. Keep `.env` root:root 0600.
   From `/opt` (outside the checkout), run:

   ```sh
   cd /opt
   sudo docker compose -f /opt/libredesk/compose.yml config --quiet
   sudo docker compose -f /opt/libredesk/compose.yml up -d --no-deps --wait app
   sudo docker compose -f /opt/libredesk/compose.yml ps --format '{{.Service}} {{.Health}}'
   sudo docker inspect --format '{{.Config.Image}}' libredesk-app
   ```

3. Require healthy `app`, `db`, `redis` and the expected running digest.
   From a public client, run `curl -fsS -o /dev/null -w '%{http_code}\n' https://support.liupengliang.com/`
   (expect `200`); smoke-test login and the changed functionality.
4. If validation fails, stop rollout. For compatible schemas, restore the
   previous `LIBREDESK_IMAGE` and repeat the app-only recreation and checks.
   If migrations are incompatible, stop app writes and coordinate restoration
   of the matching pre-upgrade database, uploads and configuration before
   starting the old image. Restoring a backup can discard later writes.

## Access and verification

The checkout is `orca:orca` 0700. The parent remains `root:root` 0750 with
a non-inherited access ACL granting `u:orca:--x` (traversal only). Without
sudo, `orca` can enter `source` but cannot list the parent or read `.env`.
To reapply traversal on a replacement host with ACL tools:
`sudo setfacl -m u:orca:--x /opt/libredesk`. Do not apply it recursively.

The remote is `github-libredesk:Allen-LPL/libredesk.git`. The SSH alias in
`/home/orca/.ssh/config` uses `HostName github.com`, `User git`,
`IdentityFile ~/.ssh/libredesk-deploy`, `IdentitiesOnly yes`, `IdentityAgent none`
and strict host key checking. `~/.ssh` is 0700; key and config are 0600.
Only `libredesk-deploy.pub` is registered as writable GitHub deploy key
`rep-dev-libredesk-source`. On retry compare its fingerprint with the
repository key before registering anything; never copy the private key.

```sh
cd /opt/libredesk/source
git remote get-url origin
git fetch origin main
git branch -vv
git push --dry-run origin HEAD:refs/heads/verify-rep-dev-source
```

The last command checks receive-pack authorization without updating a ref.
If access fails, check the SSH alias, verified GitHub host key and repository
deploy-key permissions from the owner workstation. Build tools and GitHub CLI
are unnecessary on rep-dev.
