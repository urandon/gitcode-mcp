# Release Process

GitCode is the source of truth for code review, issues, and tags. GitHub Actions
owns release automation and binary storage. The GitCode release is a metadata
mirror that receives the rendered GitHub CI release notes and links to the
GitHub-hosted assets.

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
python3 -m unittest scripts/release/test_generate_notes.py
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
   API using the same Markdown file and links to the GitHub-hosted assets.

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
python3 -m unittest scripts/release/test_generate_notes.py
```

The generator emits deterministic Markdown and reports its SHA-256 fingerprint.
The release workflow records that fingerprint in the GitHub Actions job summary
before either release publisher runs.

## Verification

After GitHub publishes the release:

1. Confirm the release commit matches the GitCode tag commit.
2. Download the target archive and `checksums.txt`.
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

The automated flow stores binary artifacts in GitHub Releases and publishes
them to GitCode as Markdown links in the release body. Both GitHub and GitCode
consume `dist/release-notes.md`; reruns edit the existing release for the same
tag and replace assets instead of creating duplicates. The browser-oriented
GitCode v2 release API uses a different auth surface and is not suitable for
GitHub Actions PAT automation. Direct GitCode binary attachment upload uses a
separate pre-signed attachment flow and should be enabled only after a live
compatibility probe.

## GitCode Token

Create a dedicated GitCode bot or service account token for release publishing. Give that account access only to this repository, with the minimum project role that can create/update releases and tags. The GitCode frontend maps release creation to tag creation permission, so a read-only or reporter-like token is not enough.

Store the token in the GitHub mirror as a repository Actions secret named `GITCODE_TOKEN`. The release workflow only reads it in the tag-triggered release job; pull request CI does not receive it.
