#!/usr/bin/env bash
set -euo pipefail
umask 077

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <immutable-image> <output-file>" >&2
  exit 2
fi

image=$1
output=$2

if [[ -e "$output" ]]; then
  echo "refusing to overwrite $output" >&2
  exit 1
fi

case "$image" in
  ghcr.io/*/libredesk:sha-*) ;;
  *)
    echo "image must be an immutable GHCR SHA tag" >&2
    exit 1
    ;;
esac

{
  printf 'LIBREDESK_IMAGE=%s\n' "$image"
  printf 'POSTGRES_PASSWORD=%s\n' "$(openssl rand -hex 24)"
  printf 'REDIS_PASSWORD=%s\n' "$(openssl rand -hex 24)"
  printf 'LIBREDESK_ENCRYPTION_KEY=%s\n' "$(openssl rand -hex 16)"
  printf 'LIBREDESK_SYSTEM_USER_PASSWORD=Aa1!%s\n' "$(openssl rand -hex 20)"
} >"$output"

chmod 0600 "$output"
