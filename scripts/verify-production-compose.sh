#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

compose=deploy/production/compose.yml
config=deploy/production/config.toml
env_example=deploy/production/.env.example
generator=deploy/production/generate-env.sh

for file in "$compose" "$config" "$env_example" "$generator"; do
  test -f "$file" || { echo "missing $file" >&2; exit 1; }
done

export LIBREDESK_IMAGE=ghcr.io/allen-lpl/libredesk:sha-test
export POSTGRES_PASSWORD=test-postgres-password
export REDIS_PASSWORD=test-redis-password
export LIBREDESK_ENCRYPTION_KEY=0123456789abcdef0123456789abcdef
export LIBREDESK_SYSTEM_USER_PASSWORD=test-system-password

rendered=$(mktemp)
trap 'rm -f "$rendered"' EXIT
docker compose -f "$compose" config >"$rendered"

ruby - "$rendered" "$config" "$env_example" <<'RUBY'
require "yaml"

compose = YAML.safe_load(File.read(ARGV[0]), aliases: true)
config = File.read(ARGV[1])
env_example = File.read(ARGV[2])
services = compose.fetch("services")
abort "expected app, db, and redis services" unless %w[app db redis].all? { |name| services.key?(name) }

app = services.fetch("app")
abort "app image is not configurable" unless app["image"] == ENV.fetch("LIBREDESK_IMAGE")
ports = app.fetch("ports")
published = ports.map { |port| [port["host_ip"], port["published"].to_s, port["target"].to_s] }
abort "app must bind only to the Tailscale address" unless published == [["100.64.0.27", "9000", "9000"]]

%w[db redis].each do |name|
  service = services.fetch(name)
  abort "#{name} must not publish host ports" if service.key?("ports")
end

services.each do |name, service|
  abort "#{name} needs restart policy" unless service["restart"] == "unless-stopped"
  abort "#{name} needs a healthcheck" unless service.key?("healthcheck")
end

mounts = app.fetch("volumes")
abort "uploads must be persistent" unless mounts.any? { |mount| mount["target"] == "/libredesk/uploads" }
abort "PostgreSQL data must be persistent" unless compose.fetch("volumes").key?("postgres-data")
abort "Redis data must be persistent" unless compose.fetch("volumes").key?("redis-data")

abort "production config must trust X-Client-IP" unless config.include?('client_ip_header = "X-Client-IP"')
abort "secure cookies must remain enabled" unless config.include?("disable_secure_cookies = false")
abort "upload path must be persistent" unless config.include?("upload_path = '/libredesk/uploads'")

def active_config_keys(contents)
  section = ""
  contents.lines.map do |line|
    stripped = line.strip
    next if stripped.empty? || stripped.start_with?("#")
    if stripped.match?(/^\[[^\]]+\]$/)
      section = stripped
      next
    end
    next unless stripped.include?("=")
    "#{section}.#{stripped.split("=", 2).first.strip}"
  end.compact
end

sample_keys = active_config_keys(File.read("config.sample.toml"))
production_keys = active_config_keys(config)
missing_keys = sample_keys - production_keys
abort "production config is missing active sample keys: #{missing_keys.join(", ")}" unless missing_keys.empty?

%w[
  LIBREDESK_IMAGE
  POSTGRES_PASSWORD
  REDIS_PASSWORD
  LIBREDESK_ENCRYPTION_KEY
  LIBREDESK_SYSTEM_USER_PASSWORD
].each do |name|
  line = env_example.lines.find { |candidate| candidate.start_with?("#{name}=") }
  abort "missing #{name} placeholder" unless line
  value = line.split("=", 2)[1].to_s.strip
  abort "#{name} has an insecure default" unless value.empty? || value.start_with?("<")
end

puts "production compose checks passed"
RUBY

generated=$(mktemp)
trap 'rm -f "$rendered" "$generated"' EXIT
rm -f "$generated"
bash "$generator" ghcr.io/allen-lpl/libredesk:sha-test "$generated"

ruby - "$generated" <<'RUBY'
values = File.readlines(ARGV[0], chomp: true).to_h do |line|
  key, value = line.split("=", 2)
  [key, value]
end

password = values.fetch("LIBREDESK_SYSTEM_USER_PASSWORD")
abort "System password must contain uppercase" unless password.match?(/[A-Z]/)
abort "System password must contain lowercase" unless password.match?(/[a-z]/)
abort "System password must contain a number" unless password.match?(/[0-9]/)
abort "System password must contain a special character" unless password.match?(/[^A-Za-z0-9]/)
abort "System password must be 10-72 characters" unless (10..72).cover?(password.length)
abort "encryption key must be exactly 32 characters" unless values.fetch("LIBREDESK_ENCRYPTION_KEY").length == 32

puts "production secret generation checks passed"
RUBY
