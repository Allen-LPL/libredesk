#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

http=deploy/nginx/support.liupengliang.com.http.conf
https=deploy/nginx/support.liupengliang.com.conf
backup=deploy/production/backup.sh

for file in "$http" "$https" "$backup"; do
  test -f "$file" || { echo "missing $file" >&2; exit 1; }
done

assert_contains() {
  local file=$1
  local text=$2
  grep -Fq -- "$text" "$file" || {
    echo "$file does not contain: $text" >&2
    exit 1
  }
}

assert_contains "$http" 'server_name support.liupengliang.com;'
assert_contains "$http" 'location ^~ /.well-known/acme-challenge/'
assert_contains "$http" 'root /var/lib/letsencrypt;'

assert_contains "$https" 'return 301 https://$host$request_uri;'
assert_contains "$https" 'proxy_pass http://100.64.0.27:9000;'
assert_contains "$https" 'proxy_set_header X-Client-IP $remote_addr;'
assert_contains "$https" 'proxy_set_header Upgrade $http_upgrade;'
assert_contains "$https" 'proxy_set_header Connection "upgrade";'
assert_contains "$https" 'client_max_body_size 100M;'
assert_contains "$https" 'proxy_read_timeout 300s;'
assert_contains "$https" 'proxy_send_timeout 300s;'

assert_contains "$backup" 'umask 077'
assert_contains "$backup" 'pg_dump'
assert_contains "$backup" 'uploads'
assert_contains "$backup" 'config.toml'
assert_contains "$backup" 'image-manifest.txt'
assert_contains "$backup" '-mtime +7'

if grep -Fq -- 'trimPrefix' "$backup"; then
  echo "$backup uses unsupported Docker template function trimPrefix" >&2
  exit 1
fi

bash -n "$backup"
echo "production operations checks passed"
