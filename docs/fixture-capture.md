# Fixture Capture

## Purpose

Fixtures provide sanitized, offline-compatible API response samples for testing and documentation. All tests pass without network access because they use fixtures.

## Fixture directory structure

```text
fixtures/
  api/
    v5/
      repos/
        <owner>/
          <repo>/
            issues.json       # list of issues
            issues/
              42.json          # single issue
              42/
                comments.json   # issue comments
            wiki/
              pages.json       # list of wiki pages
              Home.json        # single wiki page
```

## Fixture properties

- Fixtures are stored as JSON files matching the GitCode API v5 response format.
- All real tokens, credentials, cookies, and private hostnames are replaced with sanitized placeholders.
- Fixture data uses example values (`example-owner`, `example-repo`, `https://api.example.com`).

## Creating fixtures

### From live API responses

```sh
GITCODE_LIVE_TEST=1 go test -run Live -count=1 ./internal/gitcode/
```

Live responses are captured, redacted, and written to the fixture tree. The redaction pipeline replaces:

- Tokens and authorization headers with `<REDACTED_TOKEN>`
- Private hostnames with `<REDACTED_HOST>`
- Cookie headers with `<REDACTED_SECRET>`
- Raw response bodies containing secrets with `<REDACTED_RESPONSE>`

### Manual fixture creation

Fixtures can be created by hand using the documented JSON schema. Ensure:

- No real credentials, tokens, or private URLs.
- Repository identifiers are generic (`example-owner/example-repo`).
- API base URL is `https://api.example.com/api/v5` or `https://api.gitcode.com/api/v5`.
- Issue and wiki identifiers use generic values (`ISSUE-42`, `WIKI-HOME`, `Home`).

## Fixture validation

Run the offline test suite before committing fixture changes:

```sh
go test ./...
```

Fixture tests must pass without credentials or live network access. Optional live fixture refresh work should be reported on the relevant issue or pull request with sanitized summaries only.

## Public-safety rules

- Never commit API responses containing real tokens, auth headers, or private paths.
- Always run fixture validation before committing fixture changes.
- Always redact live responses before writing durable artifacts.
- Do not reference non-public repository names in fixture data.
