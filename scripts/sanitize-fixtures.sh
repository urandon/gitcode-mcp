#!/usr/bin/env bash
set -euo pipefail

usage() {
  printf 'usage: %s <raw_dir> <output_dir> --owner <raw_owner> --repo <raw_repo> --project <raw_project> --host <raw_host> [--host <raw_host>...]\n' "$0" >&2
}

if [[ $# -lt 10 ]]; then
  usage
  exit 2
fi

raw_dir=$1
output_dir=$2
shift 2

owner=
repo=
project=
hosts=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --owner)
      [[ $# -ge 2 ]] || { usage; exit 2; }
      owner=$2
      shift 2
      ;;
    --repo)
      [[ $# -ge 2 ]] || { usage; exit 2; }
      repo=$2
      shift 2
      ;;
    --project)
      [[ $# -ge 2 ]] || { usage; exit 2; }
      project=$2
      shift 2
      ;;
    --host)
      [[ $# -ge 2 ]] || { usage; exit 2; }
      hosts+=("$2")
      shift 2
      ;;
    *)
      usage
      exit 2
      ;;
  esac
done

if [[ -z "$owner" || -z "$repo" || -z "$project" || ${#hosts[@]} -eq 0 ]]; then
  usage
  exit 2
fi

if [[ ! -d "$raw_dir" ]]; then
  printf 'raw_dir does not exist or is not a directory: %s\n' "$raw_dir" >&2
  exit 1
fi

mkdir -p "$output_dir/api/v5"

python3 - "$raw_dir" "$output_dir" "$owner" "$repo" "$project" "${hosts[@]}" <<'PY'
import json
import os
import re
import shutil
import sys
from pathlib import Path

raw_dir = Path(sys.argv[1]).resolve()
output_dir = Path(sys.argv[2]).resolve()
owner = sys.argv[3]
repo = sys.argv[4]
project = sys.argv[5]
hosts = sys.argv[6:]

replacements = [
    (owner, "example-owner"),
    (repo, "example-repo"),
    (project, "example-project"),
]
replacements.extend((host, "api.example.com") for host in hosts)

authorization_line = re.compile(r"(?im)^\s*authorization\s*:[^\r\n]*(?:\r?\n)?")
authorization_present = re.compile(r"(?i)authorization")
host_pattern = re.compile(r"(?i)\b(?:[a-z0-9-]+\.)+[a-z]{2,}\b")
allowed_suffixes = {".json", ".txt", ".http", ".har", ".headers", ".body", ".log"}


def replace_tokens(value):
    for old, new in replacements:
        if old:
            value = value.replace(old, new)
    return value


def strip_authorization(obj):
    if isinstance(obj, dict):
        return {k: strip_authorization(v) for k, v in obj.items() if k.lower() != "authorization"}
    if isinstance(obj, list):
        return [strip_authorization(item) for item in obj]
    return obj


def sanitize_json(text, source):
    stripped = authorization_line.sub("", text)
    try:
        decoded = json.loads(stripped)
    except json.JSONDecodeError:
        if authorization_present.search(stripped):
            raise SystemExit(f"refusing to emit structurally ambiguous Authorization field in {source}")
        return replace_tokens(stripped)
    decoded = strip_authorization(decoded)
    encoded = json.dumps(decoded, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
    return replace_tokens(encoded)


def sanitize_text(text, source):
    sanitized = authorization_line.sub("", text)
    if authorization_present.search(sanitized):
        raise SystemExit(f"refusing to emit structurally ambiguous Authorization field in {source}")
    return replace_tokens(sanitized)


def output_relative_path(path):
    relative = path.relative_to(raw_dir)
    parts = list(relative.parts)
    if "api" in parts:
        idx = parts.index("api")
        if idx + 1 < len(parts) and parts[idx + 1] == "v5":
            relative = Path(*parts[idx + 2:]) if idx + 2 < len(parts) else Path(path.name)
    elif "v5" in parts:
        idx = parts.index("v5")
        relative = Path(*parts[idx + 1:]) if idx + 1 < len(parts) else Path(path.name)
    rel_text = replace_tokens(relative.as_posix())
    return Path("api") / "v5" / rel_text


def verify_output(path, content):
    text_path = path.relative_to(output_dir).as_posix()
    check = f"{text_path}\n{content}"
    forbidden = [token for token in [owner, repo, project, *hosts] if token and token in check]
    if forbidden:
        raise SystemExit(f"sanitized output contains raw token {forbidden[0]!r}: {path}")
    if authorization_present.search(check):
        raise SystemExit(f"sanitized output contains Authorization: {path}")
    for host in host_pattern.findall(content):
        if host != "api.example.com":
            raise SystemExit(f"sanitized output contains disallowed hostname {host!r}: {path}")

for source in sorted(raw_dir.rglob("*")):
    if not source.is_file():
        continue
    if source.suffix.lower() not in allowed_suffixes:
        continue
    try:
        raw_text = source.read_text(encoding="utf-8")
    except UnicodeDecodeError as exc:
        raise SystemExit(f"fixture capture is not UTF-8 text: {source}") from exc
    if source.suffix.lower() == ".json":
        sanitized = sanitize_json(raw_text, source)
    else:
        sanitized = sanitize_text(raw_text, source)
    rel = output_relative_path(source)
    dest = output_dir / rel
    dest.parent.mkdir(parents=True, exist_ok=True)
    verify_output(dest, sanitized)
    dest.write_text(sanitized, encoding="utf-8")

if not any((output_dir / "api" / "v5").rglob("*")):
    raise SystemExit("no supported fixture files were sanitized")
PY
