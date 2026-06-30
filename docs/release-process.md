# Release Process

GitCode is the source of truth for code review, issues, and tags. GitHub is a release and CI mirror.

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
git diff --check
```

3. Create and push a tag in GitCode:

```sh
git tag v0.1.0
git push origin v0.1.0
```

4. GitCode push mirroring propagates the tag to GitHub.
5. GitHub Actions runs the release workflow from the mirrored tag.
6. The workflow publishes GitHub Release assets and `checksums.txt`.

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

## Verification

After GitHub publishes the release:

1. Confirm the release commit matches the GitCode tag commit.
2. Download the target archive and `checksums.txt`.
3. Verify the checksum.
4. Run `gitcode-mcp --version`.

Release assets must not include local config, credentials, cache files, or repository-local `.gitcode/mcp` data.
