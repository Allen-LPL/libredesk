#!/usr/bin/env bash
set -euo pipefail
umask 077

root=/opt/libredesk
backup_dir="$root/backups"
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
tmp=$(mktemp -d "$backup_dir/.tmp-${timestamp}-XXXXXX")
archive="$backup_dir/libredesk-${timestamp}.tar.gz"

cleanup() {
  rm -rf "$tmp"
}
trap cleanup EXIT

cd "$root"

docker compose exec -T db \
  pg_dump -U libredesk -d libredesk --format=custom \
  >"$tmp/database.dump"

{
  printf 'created_at=%s\n' "$timestamp"
  docker compose ps -q |
    xargs -r docker inspect \
      --format '{{.Name}} {{.Config.Image}} {{.Image}}' |
    sed 's#^/##'
} >"$tmp/image-manifest.txt"

tar -C "$root" -czf "$archive" \
  compose.yml config.toml .env uploads \
  -C "$tmp" database.dump image-manifest.txt

chmod 600 "$archive"
find "$backup_dir" -maxdepth 1 -type f -name 'libredesk-*.tar.gz' -mtime +7 -delete

printf 'backup=%s size_bytes=%s\n' \
  "$(basename "$archive")" \
  "$(stat -c %s "$archive")"
