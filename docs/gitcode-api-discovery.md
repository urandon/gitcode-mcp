# GitCode API Discovery

## Purpose

Record what the tracker/wiki API can actually do before broad migration.

## Questions

- Which official or internal API docs are available?
- How are tracker issues created, updated, searched, labeled, and commented?
- How are wiki pages created, updated, searched, moved, and linked?
- How are attachments represented?
- What auth modes are supported?
- What pagination, rate limit, and timeout behavior exists?
- Are issue ids stable across project moves or imports?
- Can backlinks be created or discovered through API?
- Can we export enough state for rollback and audit?

## Evidence Rules

- Never commit credentials or private tokens.
- Prefer sanitized request/response fixtures.
- Record API version, date, host, and permission scope.
- Separate official docs from reverse-engineered behavior.
- Mark uncertain behavior explicitly.
