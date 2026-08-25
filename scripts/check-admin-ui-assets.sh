#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_dir/web"
npm ci
npm run licenses
npm run build
cd "$repo_dir"
git diff --exit-code -- internal/adminui/assets web/package-lock.json
