#!/usr/bin/env python3
"""Wait until an exact release tag has reached the public GitCode mirror."""

from __future__ import annotations

import argparse
import subprocess
import sys
import time
import urllib.parse
from collections.abc import Callable


class TagWaitError(RuntimeError):
    """A public-safe tag mirror readiness failure."""


def _validate_inputs(repo_url: str, tag: str, expected_commit: str) -> None:
    parsed = urllib.parse.urlparse(repo_url)
    if parsed.scheme != "https" or not parsed.netloc:
        raise TagWaitError("GitCode tag wait requires an HTTPS repository URL")
    if not tag.startswith("v") or any(char.isspace() for char in tag):
        raise TagWaitError("GitCode tag wait requires a v-prefixed tag without whitespace")
    if len(expected_commit) != 40 or any(char not in "0123456789abcdef" for char in expected_commit.lower()):
        raise TagWaitError("GitCode tag wait requires a full 40-character commit SHA")


def _remote_commits(output: str) -> set[str]:
    commits: set[str] = set()
    for line in output.splitlines():
        fields = line.split()
        if len(fields) == 2 and len(fields[0]) == 40:
            commits.add(fields[0].lower())
    return commits


def wait_for_tag(
    *,
    repo_url: str,
    tag: str,
    expected_commit: str,
    timeout: float = 300,
    interval: float = 5,
    run: Callable[..., subprocess.CompletedProcess[str]] = subprocess.run,
    monotonic: Callable[[], float] = time.monotonic,
    sleep: Callable[[float], None] = time.sleep,
) -> int:
    _validate_inputs(repo_url, tag, expected_commit)
    if timeout <= 0 or timeout > 1800:
        raise TagWaitError("GitCode tag wait timeout must be greater than zero and at most 1800 seconds")
    if interval <= 0 or interval > 60:
        raise TagWaitError("GitCode tag wait interval must be greater than zero and at most 60 seconds")

    expected = expected_commit.lower()
    deadline = monotonic() + timeout
    attempts = 0
    tag_ref = f"refs/tags/{tag}"
    peeled_ref = f"{tag_ref}^{{}}"
    while True:
        attempts += 1
        completed = run(
            ["git", "ls-remote", "--exit-code", "--tags", repo_url, tag_ref, peeled_ref],
            check=False,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
        )
        commits = _remote_commits(completed.stdout)
        if expected in commits:
            return attempts
        if commits:
            raise TagWaitError("GitCode tag exists but does not resolve to the expected release commit")
        if monotonic() >= deadline:
            raise TagWaitError("GitCode tag mirror did not become ready before the bounded timeout")
        sleep(interval)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo-url", required=True)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--expected-commit", required=True)
    parser.add_argument("--timeout", type=float, default=300)
    parser.add_argument("--interval", type=float, default=5)
    args = parser.parse_args()
    try:
        attempts = wait_for_tag(
            repo_url=args.repo_url,
            tag=args.tag,
            expected_commit=args.expected_commit,
            timeout=args.timeout,
            interval=args.interval,
        )
    except TagWaitError as exc:
        print(f"gitcode tag wait: {exc}", file=sys.stderr)
        return 1
    print(f"gitcode tag ready: tag={args.tag} attempts={attempts}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
