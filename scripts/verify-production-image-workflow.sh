#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

ruby <<'RUBY'
require "yaml"

path = ".github/workflows/production-image.yml"
abort "missing #{path}" unless File.file?(path)

workflow = YAML.safe_load(File.read(path), aliases: true)
triggers = workflow["on"] || workflow[true]
push = triggers.fetch("push")
branches = push.fetch("branches")
abort "workflow must run on main pushes" unless branches.include?("main")

permissions = workflow.fetch("permissions")
abort "packages: write is required" unless permissions["packages"] == "write"
abort "contents: read is required" unless permissions["contents"] == "read"

job = workflow.fetch("jobs").fetch("build-and-push")
steps = job.fetch("steps")
commands = steps.map { |step| step["run"] }.compact.join("\n")
abort "Go tests must run" unless commands.include?("go test -count=1 ./...")
abort "frontend tests must run" unless commands.include?("pnpm test:run")
abort "GHCR owner must derive from repository owner" unless commands.include?("GITHUB_REPOSITORY_OWNER,,")

build = steps.find { |step| step["uses"].to_s.start_with?("docker/build-push-action@") }
abort "Docker build-push step is required" unless build
tags = build.fetch("with").fetch("tags")
abort "immutable SHA tag is required" unless tags.include?("sha-${{ github.sha }}")
abort "main tag is required" unless tags.include?(":main")
abort "prepared GHCR image name must be used" unless tags.include?("${{ env.IMAGE_NAME }}")

puts "production image workflow checks passed"
RUBY
