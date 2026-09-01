# Release Process

GitCode is the source of truth for code review, issues, tags, and native release
downloads. GitHub Actions owns release automation and also publishes a GitHub
Release mirror. Both releases receive the same rendered notes and binary set.

## Versioning

Use SemVer tags with a `v` prefix:

```sh
v0.1.0
```

The release binary prints the same version without the `v` prefix:

```sh
gitcode-mcp --version
```

```text
gitcode-mcp 0.1.0
```

## Maintainer Flow

1. Ensure `main` is up to date and clean.
2. Run:

```sh
go test ./...
python3 -m unittest discover -s scripts/release -p 'test_*.py'
git diff --check
```

3. Create and push a tag in GitCode:

```sh
git tag v0.1.0
git push origin v0.1.0
```

4. GitCode push mirroring propagates the tag to GitHub.
5. GitHub Actions runs the release workflow from the mirrored tag.
6. The workflow generates `dist/release-notes.md` from the first-parent Git
   history between the previous release tag and the current tag.
7. The workflow creates or updates the GitHub Release with those notes, binary
   assets, and `checksums.txt`.
8. If the `GITCODE_TOKEN` GitHub Actions secret is configured, the workflow
   creates or updates the matching GitCode release through the PAT-compatible
   API using the same Markdown file and GitHub fallback links.
9. The workflow requests a short-lived upload contract for each artifact,
   uploads it without forwarding the GitCode token to object storage, and
   verifies the final GitCode asset names and byte sizes.

Creating and pushing the tag remains the only required release action. Release
note generation is not part of the shipped `gitcode-mcp` binary.

## Artifacts

The first release workflow builds:

- `darwin/arm64`
- `linux/amd64`
- `linux/arm64`
- `windows/amd64`

Unix targets are published as `.tar.gz`. Windows is published as `.zip`.

## Local Dry Run

Run the release builder locally:

```sh
./scripts/release/build.sh
```

To build a single target:

```sh
GOOS=linux GOARCH=amd64 ./scripts/release/build.sh
```

The script writes artifacts to `dist/` and generates `dist/checksums.txt`.

Preview the next release notes without creating a tag:

```sh
python3 scripts/release/generate_notes.py \
  --tag v0.2.0 \
  --preview \
  --previous-tag v0.1.0 \
  --gitcode-web-base-url https://gitcode.com/YOUR_OWNER/YOUR_REPO
```

The generator reads only local Git history. GitCode merge commit subjects and
bodies preserve merge request numbers, titles, and closing issue references;
direct commits remain visible as commit links. The GitHub mirror currently does
not mirror GitCode merge request records, so GitHub native generated notes are
not the metadata source for this workflow.

For a release that needs human-written context, add an optional reviewed file at
`.github/release-notes/TAG.md`, for example
`.github/release-notes/v0.2.0.md`. CI includes it as the **Maintainer notes**
section. The generated file itself is not committed.

Run the generator fixture tests locally:

```sh
python3 -m unittest discover -s scripts/release -p 'test_*.py'
```

The generator emits deterministic Markdown and reports its SHA-256 fingerprint.
The release workflow records that fingerprint in the GitHub Actions job summary
before either release publisher runs.

## Verification

After both publishers complete:

1. Confirm the release commit matches the GitCode tag commit.
2. Confirm the GitCode **Download** list contains every archive and
   `checksums.txt`; use the GitHub mirror as a fallback.
3. Verify the checksum.
4. Run `gitcode-mcp --version`.

Release assets must not include local config, credentials, cache files, or repository-local `.gitcode/mcp` data.

## GitCode Release Publishing

GitCode releases are published by the Go CLI, not by an ad hoc shell script:

```sh
gitcode-mcp publish-release \
  --repo urandon/gitcode-mcp \
  --tag v0.1.0 \
  --ref main \
  --title "gitcode-mcp v0.1.0" \
  --input /path/to/release-notes.md \
  --asset gitcode-mcp_v0.1.0_darwin_arm64.tar.gz=https://github.com/urandon/gitcode-mcp/releases/download/v0.1.0/gitcode-mcp_v0.1.0_darwin_arm64.tar.gz \
  --status latest \
  --idempotency-key release-v0.1.0
```

The command validates with `--dry-run` and otherwise performs an idempotent create-or-update by tag:

1. `GET /api/v5/repos/{owner}/{repo}/releases/tags/{tag}`
2. `POST /api/v5/repos/{owner}/{repo}/releases` when missing
3. `PATCH /api/v5/repos/{owner}/{repo}/releases/{tag}` when present

The automated flow publishes binary artifacts to both GitHub Releases and the
native GitCode **Download** section. GitHub links remain in the Markdown as a
portable fallback. Both publishers consume `dist/release-notes.md`.

GitCode attachments use a separate presigned upload flow. The workflow first
creates or updates release metadata, requests an HTTPS upload contract for each
filename, then sends the artifact bytes with exactly the returned object-store
headers. It never forwards `GITCODE_TOKEN` to the presigned URL and never prints
the URL or contract headers. API calls, long-running object uploads, and small
object-size readbacks have separate bounded time budgets. Replay-safe contract
reads, attachment reads, and presigned `PUT` uploads use limited
exponential-backoff retries; other methods are never retried implicitly. A
rerun skips an existing name only when its byte size matches; a mismatch fails
closed. Size readback prefers a one-byte HTTPS range request and its total
object length, avoiding a second full download on a poor network. The final
release asset inventory is polled for a bounded period and verified by name and
size.

## GitCode Token

Create a dedicated GitCode bot or service account token for release publishing. Give that account access only to this repository, with the minimum project role that can create/update releases and tags. The GitCode frontend maps release creation to tag creation permission, so a read-only or reporter-like token is not enough.

Store the token in the GitHub mirror as a repository Actions secret named `GITCODE_TOKEN`. The release workflow only reads it in the tag-triggered release job; pull request CI does not receive it.
