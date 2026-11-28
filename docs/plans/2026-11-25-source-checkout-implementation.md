# Editable source checkout implementation plan

**Goal:** Let operators edit and push from rep-dev without deploying their working copy.

**Architecture:** Git over dedicated SSH to GitHub; Actions to GHCR; manual
image promotion to Compose. Production credentials stay outside the checkout.

1. Start from current `origin/main`. Inspect ownership, current mounts, image
   and health without rendering Compose secrets.
2. Under `/home/orca/.ssh`, create or reuse the dedicated ED25519 key and
   `github-libredesk` alias. Verify GitHub's SSH host key. From the owner
   workstation register the matching public key once with write access.
3. Add an access ACL `u:orca:--x` to `/opt/libredesk` only. Create `source`
   as `orca:orca` mode `0700`; clone `main` as `orca` via the alias.
4. Confirm the tracking branch, clean status, remote read, and dry-run push;
   verify ordinary `orca` cannot list the parent or read `.env`.
5. Confirm unchanged app mounts, healthy app/db/redis and public HTTP 200.
   Publish the [operator runbook](../source-on-rep-dev.md) in a reviewed PR
   after running `scripts/verify-production-{compose,image-workflow,ops}.sh`.
