# Validation Scenarios: Repo Registry Add

## 002-repo-binding-task-1-repo-registry-add-scenario-1

A developer runs `gitcode-mcp repo add --repo fixture-a --owner owner-a --name repo-a --api-base-url https://example.invalid/api --scopes issues,wiki --alias proj` and then `gitcode-mcp repo status --repo fixture-a`; the CLI reports configured metadata, enabled scopes, repository aliases, public-safe API base URL, `binding_state: ready`, and cache/index handoff fields without token or private-path disclosure.

Concrete offline checks:

- Build the real `cmd/gitcode-mcp` CLI binary from the current working tree.
- Use isolated temporary `HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, `GOCACHE`, and cache database paths.
- Run the real CLI product path `repo add` for `fixture-a` with sanitized owner/name/API base URL/scopes/alias inputs.
- Run `repo status --repo fixture-a` against the same temporary cache.
- Assert stdout includes `repo_id`, `owner`, `name`, public-safe `api_base_url`, normalized `scopes`, `aliases`, `binding_state: ready`, `alias_conflict_state: none`, `cache_state`, `index_state`, and a non-secret failure/handoff class.
- Assert stdout/stderr do not contain known token/userinfo/private-path sentinels.

## 002-repo-binding-task-1-repo-registry-add-scenario-2

A developer then runs `repo add` with an existing `repo_id` or colliding repository alias and receives a typed conflict, nonzero exit status, and no partial repo or alias row.

Concrete offline checks:

- Add `fixture-a` once, then attempt to add `fixture-a` again with different metadata.
- Assert the duplicate `repo_id` command exits nonzero and reports a typed conflict diagnostic.
- Attempt to add `fixture-b` with repository alias `proj`, already owned by `fixture-a`.
- Assert the alias collision command exits nonzero and reports a typed conflict diagnostic.
- Run `repo status --repo fixture-b` and assert it returns typed not-found, proving no partial repository row was committed after the alias collision.
- Run `repo status --repo fixture-a` again and assert its original owner/name/alias remain intact.

## 002-repo-binding-task-1-repo-registry-add-scenario-3

A developer also runs `repo status --repo missing-repo` and receives a typed not-found diagnostic with actionable failure class.

Concrete offline checks:

- Run the real CLI `repo status --repo missing-repo` against the temporary cache.
- Assert it exits with the not-found exit status and stderr includes repository not-found wording plus an actionable failure class.

## 002-repo-binding-task-1-repo-registry-add-scenario-4

Executable evidence is a CLI integration test using temporary config/cache state with two sanitized fixture repositories and one ready status plus one diagnostic failure state.

Concrete offline checks:

- Use only sanitized fixture repository IDs (`fixture-a`, `fixture-b`) and sanitized public placeholder hostnames.
- Exercise the real built CLI binary through shell commands, not grep-only source inspection.
- Keep all state under a temporary directory and do not require GitCode credentials, live network, OS keyring, or device validation.
- Fail nonzero on any missing product field, wrong exit code, conflict/not-found diagnostic regression, partial-row regression, or public-safety leak.
