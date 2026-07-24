#!/usr/bin/env python3
"""Generate deterministic release notes for the GitHub release workflow."""

from __future__ import annotations

import argparse
import dataclasses
import hashlib
import re
import subprocess
import sys
from pathlib import Path
from urllib.parse import urlparse


GIT_LOG_FORMAT = "%H%x1f%s%x1f%b%x1e"
SEMVER_TAG = re.compile(
    r"^v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)"
    r"(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?"
    r"(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$"
)
MERGE_REQUEST_SUBJECT = re.compile(r"^!(\d+)\s+merge\b", re.IGNORECASE)
CLOSING_CLAUSE = re.compile(
    r"\b(?:close(?:s|d)?|fix(?:es|ed)?|resolve(?:s|d)?)\s+"
    r"(#\d+(?:(?:\s*[,;]\s*|\s+and\s+)#\d+)*)",
    re.IGNORECASE,
)
ISSUE_REFERENCE = re.compile(r"#(\d+)")
BREAKING_MARKER = re.compile(
    r"^\s*(?:breaking(?:\s+change)?|breaks compatibility)\s*:",
    re.IGNORECASE | re.MULTILINE,
)


class ReleaseNotesError(RuntimeError):
    """A public-safe release note generation failure."""


@dataclasses.dataclass(frozen=True)
class Asset:
    name: str
    url: str


@dataclasses.dataclass(frozen=True)
class Entry:
    sha: str
    title: str
    merge_request: int | None
    issues: tuple[int, ...]
    labels: tuple[str, ...]
    category: str
    breaking: bool


@dataclasses.dataclass(frozen=True)
class GenerationResult:
    tag: str
    previous_tag: str
    target: str
    preview: bool
    entries: tuple[Entry, ...]
    content: str
    fingerprint: str


class GitRepository:
    def __init__(self, path: Path) -> None:
        self.path = path

    def output(self, *args: str, check: bool = True) -> str:
        completed = subprocess.run(
            ["git", "-C", str(self.path), *args],
            check=False,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        if completed.returncode != 0 and check:
            operation = args[0] if args else "command"
            detail = completed.stderr.strip()
            if detail:
                raise ReleaseNotesError(
                    f"release notes: git {operation} failed: {detail}"
                )
            raise ReleaseNotesError(
                f"release notes: git {operation} failed with exit code "
                f"{completed.returncode}"
            )
        if completed.returncode != 0:
            return ""
        return completed.stdout


def validate_tag(tag: str, field: str = "tag") -> str:
    value = tag.strip()
    if not SEMVER_TAG.fullmatch(value):
        raise ReleaseNotesError(
            f"release notes: {field} {value!r} must be a v-prefixed SemVer tag"
        )
    return value


def verify_commit(git: GitRepository, ref: str, field: str) -> None:
    if not git.output(
        "rev-parse", "--verify", "--quiet", f"{ref}^{{commit}}", check=False
    ).strip():
        raise ReleaseNotesError(
            f"release notes: {field} {ref!r} does not resolve to a commit"
        )


def resolve_previous_tag(
    git: GitRepository, target: str, explicit: str, preview: bool
) -> str:
    if explicit.strip():
        previous = validate_tag(explicit, "previous tag")
        verify_commit(git, previous, "previous tag")
        previous_sha = git.output("rev-parse", f"{previous}^{{commit}}").strip()
        first_parent_shas = set(
            git.output("rev-list", "--first-parent", target).splitlines()
        )
        if previous_sha not in first_parent_shas:
            raise ReleaseNotesError(
                f"release notes: previous tag {previous!r} is not on the "
                f"first-parent history of {target!r}"
            )
        return previous

    describe_target = target if preview else f"{target}^"
    previous = git.output(
        "describe",
        "--tags",
        "--match=v*",
        "--abbrev=0",
        "--first-parent",
        describe_target,
        check=False,
    ).strip()
    if previous:
        validate_tag(previous, "previous tag")
    return previous


def one_line(value: str) -> str:
    return " ".join(value.strip().split())


def merge_request_number(subject: str) -> int | None:
    match = MERGE_REQUEST_SUBJECT.match(subject)
    if not match:
        return None
    number = int(match.group(1))
    return number if number > 0 else None


def first_body_title(body: str) -> str:
    metadata_prefixes = (
        "created-by:",
        "commit-by:",
        "merged-by:",
        "description:",
        "see merge request:",
        "label:",
        "labels:",
    )
    for raw_line in body.splitlines():
        line = one_line(raw_line)
        if not line:
            continue
        lower = line.lower()
        if lower.startswith(metadata_prefixes):
            continue
        if line.startswith(("#", "-", "*")):
            continue
        return line
    return ""


def extract_issues(body: str) -> tuple[int, ...]:
    issues: set[int] = set()
    for clause in CLOSING_CLAUSE.finditer(body):
        for raw_number in ISSUE_REFERENCE.findall(clause.group(1)):
            number = int(raw_number)
            if number > 0:
                issues.add(number)
    return tuple(sorted(issues))


def extract_labels(body: str) -> tuple[str, ...]:
    labels: set[str] = set()
    for raw_line in body.splitlines():
        line = raw_line.strip()
        if not re.match(r"^labels?\s*:", line, re.IGNORECASE):
            continue
        _, raw_values = line.split(":", 1)
        for raw_label in re.split(r"[,;]", raw_values):
            label = raw_label.strip().lower()
            if label:
                labels.add(label)
    return tuple(sorted(labels))


def contains_word(value: str, *words: str) -> bool:
    return any(
        re.search(rf"\b{re.escape(word)}\b", value, re.IGNORECASE) is not None
        for word in words
    )


def classify(title: str, labels: tuple[str, ...]) -> str:
    label_set = set(labels)
    if label_set.intersection({"ci", "release"}):
        return "ci"
    if label_set.intersection({"bug", "bugfix", "fix"}):
        return "fixed"
    if label_set.intersection({"enhancement", "feature"}):
        return "added"
    if contains_word(title, "release", "workflow", "artifact", "checksum", "ci"):
        return "ci"
    if contains_word(
        title, "fix", "fixed", "bug", "stabilize", "correct", "failure", "error",
        "regression",
    ):
        return "fixed"
    if contains_word(
        title, "add", "support", "introduce", "create", "implement", "expose",
    ):
        return "added"
    return "changed"


def parse_entries(raw: str) -> tuple[Entry, ...]:
    entries: list[Entry] = []
    for raw_record in raw.split("\x1e"):
        record = raw_record.strip(" \t\r\n")
        if not record:
            continue
        parts = record.split("\x1f", 2)
        if len(parts) != 3:
            raise ReleaseNotesError(
                "release notes: malformed git log record in selected range"
            )
        sha = parts[0].strip()
        subject = one_line(parts[1])
        body = parts[2].strip()
        merge_request = merge_request_number(subject)
        title = first_body_title(body) if merge_request is not None else subject
        if not title:
            title = subject
        labels = extract_labels(body)
        entries.append(
            Entry(
                sha=sha,
                title=title,
                merge_request=merge_request,
                issues=extract_issues(body),
                labels=labels,
                category=classify(title, labels),
                breaking=BREAKING_MARKER.search(f"{title}\n{body}") is not None,
            )
        )
    return tuple(entries)


def markdown_text(value: str) -> str:
    return (
        one_line(value)
        .replace("\\", "\\\\")
        .replace("[", "\\[")
        .replace("]", "\\]")
    )


def render_entry(entry: Entry, web_base_url: str) -> str:
    references: list[str] = []
    if entry.merge_request is not None:
        references.append(
            f"[!{entry.merge_request}]"
            f"({web_base_url}/merge_requests/{entry.merge_request})"
        )
    else:
        short_sha = entry.sha[:8]
        references.append(f"[{short_sha}]({web_base_url}/commit/{entry.sha})")
    for issue in entry.issues:
        references.append(f"[#{issue}]({web_base_url}/issues/{issue})")

    line = f"- {markdown_text(entry.title)}"
    if references:
        line += f" ({', '.join(references)})"
    if entry.labels:
        rendered_labels = ", ".join(
            f"`{label.replace('`', '')}`" for label in entry.labels
        )
        line += f" — {rendered_labels}"
    return line


def render_notes(
    *,
    tag: str,
    previous_tag: str,
    preview: bool,
    entries: tuple[Entry, ...],
    web_base_url: str,
    supplement: str,
    assets: tuple[Asset, ...],
    highlight_count: int,
) -> str:
    lines = [f"# gitcode-mcp {tag}", ""]
    if previous_tag:
        lines.append(
            f"Changes since `{previous_tag}`; generated from the first-parent "
            f"Git history for `{tag}`."
        )
    else:
        lines.append(
            f"Generated from the first-parent Git history through `{tag}`."
        )
    if preview:
        lines.append("This is a pre-tag preview generated from `HEAD`.")

    if supplement.strip():
        lines.extend(["", "## Maintainer notes", "", supplement.strip()])

    lines.extend(["", "## Highlights", ""])
    for entry in entries[:highlight_count]:
        lines.append(render_entry(entry, web_base_url))

    section_categories = (
        ("Added", "added"),
        ("Changed", "changed"),
        ("Fixed", "fixed"),
        ("CI / release infrastructure", "ci"),
    )
    for title, category in section_categories:
        category_entries = [entry for entry in entries if entry.category == category]
        if not category_entries:
            continue
        lines.extend(["", f"## {title}", ""])
        lines.extend(
            render_entry(entry, web_base_url) for entry in category_entries
        )

    lines.extend(["", "## Breaking changes / migration notes", ""])
    breaking_entries = [entry for entry in entries if entry.breaking]
    if breaking_entries:
        lines.extend(
            render_entry(entry, web_base_url) for entry in breaking_entries
        )
    else:
        lines.append("- None identified.")

    lines.extend(["", "## Assets", ""])
    if assets:
        for asset in sorted(assets, key=lambda item: (item.name, item.url)):
            lines.append(f"- [{markdown_text(asset.name)}]({asset.url})")
    else:
        lines.append("- No binary assets supplied to this preview.")

    return "\n".join(lines).rstrip() + "\n"


def parse_asset(raw: str) -> Asset:
    name, separator, url = raw.partition("=")
    name = name.strip()
    url = url.strip()
    if not separator or not name or not url:
        raise ReleaseNotesError(
            "release notes: asset must use the form name=https://example/path"
        )
    parsed = urlparse(url)
    if parsed.scheme != "https" or not parsed.netloc:
        raise ReleaseNotesError(
            f"release notes: asset URL for {name!r} must be an absolute HTTPS URL"
        )
    return Asset(name=name, url=url)


def generate(
    *,
    git: GitRepository,
    tag: str,
    previous_tag: str,
    preview: bool,
    web_base_url: str,
    supplement: str,
    assets: tuple[Asset, ...],
    highlight_count: int,
) -> GenerationResult:
    tag = validate_tag(tag)
    if highlight_count < 1:
        raise ReleaseNotesError("release notes: highlights must be at least 1")

    target = "HEAD" if preview else tag
    if not preview:
        verify_commit(git, tag, "tag")

    previous = resolve_previous_tag(git, target, previous_tag, preview)
    log_range = f"{previous}..{target}" if previous else target
    raw = git.output("log", "--first-parent", f"--format={GIT_LOG_FORMAT}", log_range)
    entries = parse_entries(raw)
    if not entries:
        raise ReleaseNotesError(
            f"release notes: no delivered changes found in {log_range!r}"
        )

    base_url = web_base_url.strip().rstrip("/")
    parsed_base = urlparse(base_url)
    if parsed_base.scheme != "https" or not parsed_base.netloc:
        raise ReleaseNotesError(
            "release notes: --gitcode-web-base-url must be an absolute HTTPS URL"
        )

    content = render_notes(
        tag=tag,
        previous_tag=previous,
        preview=preview,
        entries=entries,
        web_base_url=base_url,
        supplement=supplement,
        assets=assets,
        highlight_count=highlight_count,
    )
    fingerprint = hashlib.sha256(content.encode("utf-8")).hexdigest()
    return GenerationResult(
        tag=tag,
        previous_tag=previous,
        target=target,
        preview=preview,
        entries=entries,
        content=content,
        fingerprint=fingerprint,
    )


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description=(
            "Generate deterministic release notes from local first-parent Git "
            "history. This is a CI/development tool, not a gitcode-mcp command."
        )
    )
    parser.add_argument("--tag", required=True, help="release or planned v* SemVer tag")
    parser.add_argument("--previous-tag", default="", help="explicit comparison tag")
    parser.add_argument(
        "--preview",
        action="store_true",
        help="generate a planned release preview from HEAD",
    )
    parser.add_argument(
        "--gitcode-web-base-url",
        required=True,
        help="GitCode repository browser URL used for MR, issue, and commit links",
    )
    parser.add_argument(
        "--repo-dir", type=Path, default=Path.cwd(), help="local Git worktree"
    )
    parser.add_argument(
        "--supplement", type=Path, help="optional maintainer Markdown"
    )
    parser.add_argument(
        "--asset",
        action="append",
        default=[],
        metavar="NAME=URL",
        help="release asset link; may be repeated",
    )
    parser.add_argument(
        "--highlights", type=int, default=3, help="number of highlight entries"
    )
    parser.add_argument(
        "--output", type=Path, help="output Markdown path; defaults to stdout"
    )
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        supplement = ""
        if args.supplement is not None:
            supplement = args.supplement.read_text(encoding="utf-8")
        assets = tuple(parse_asset(raw) for raw in args.asset)
        result = generate(
            git=GitRepository(args.repo_dir),
            tag=args.tag,
            previous_tag=args.previous_tag,
            preview=args.preview,
            web_base_url=args.gitcode_web_base_url,
            supplement=supplement,
            assets=assets,
            highlight_count=args.highlights,
        )
        if args.output is None:
            sys.stdout.write(result.content)
        else:
            args.output.parent.mkdir(parents=True, exist_ok=True)
            args.output.write_text(result.content, encoding="utf-8")
            print(
                "release_notes:"
                f" tag={result.tag}"
                f" previous_tag={result.previous_tag or 'none'}"
                f" target={result.target}"
                f" preview={str(result.preview).lower()}"
                f" entries={len(result.entries)}"
                f" output={args.output}"
                f" fingerprint={result.fingerprint}"
            )
        return 0
    except (OSError, ReleaseNotesError) as error:
        print(str(error), file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
