# Config Reference

## Configuration sources

Configuration is loaded from five layers, in order of precedence:

1. Command-line overrides (`--cache-path`, `--timeout`, `--max-size`, `--format`)
2. Cache environment overrides such as `GITCODE_MCP_CACHE_DIR`
3. YAML config file (at `GITCODE_MCP_CONFIG` path or default location)
4. Repo-local config opt-in from `.gitcode/gitcode-mcp.yaml`
5. Built-in defaults

Legacy JSON config files are still recognized through `GITCODE_CONFIG`.

## Default config location

| Platform | Path |
|---|---|
| macOS | `$HOME/Library/Application Support/gitcode-mcp/config.yaml` |
| Linux | `$XDG_CONFIG_HOME/gitcode-mcp/config.yaml` (falls back to `$HOME/.config/gitcode-mcp/config.yaml`) |
| Windows | `%AppData%/gitcode-mcp/config.yaml` |

Override with the `GITCODE_MCP_CONFIG` environment variable:

```sh
export GITCODE_MCP_CONFIG=/path/to/custom/config.yaml
```

## Config file format (YAML)

```yaml
cache_path: /path/to/cache/gitcode-mcp/cache.db
lock_path: /path/to/cache/gitcode-mcp/cache.db.lock
cache_mode: global
gitcode_base_url: https://api.gitcode.com/api/v5
default_timeout: 30s
max_response_size: 10485760
max_retries: 2
format: text
mcp:
  tools:
    access: write
feedback:
  enabled: false
  sink: gitcode_issues
  repo_id: example-owner/feedback-repo
  labels: feedback|dogfood
  duplicate_policy: suggest
credential:
  store: auto
  keyring_service: gitcode-mcp
  keyring_account: token
service:
  job_retention:
    success_ttl: 48h
    diagnostic_ttl: 336h
    max_terminal_jobs: 128
    max_diagnostic_jobs: 32
    max_progress_events: 256
rag:
  model_store_path: /path/to/models/gitcode-mcp
  default_profile: qwen3-ollama-0_6b-1024
  providers:
    ollama:
      endpoint: http://127.0.0.1:11434
      data_boundary: local_network
      executable: ollama
      startup: managed
      autostart: true
      env:
        OLLAMA_MODELS: /path/to/models/ollama
      model_storage:
        mode: provider-owned
        env: OLLAMA_MODELS
  profiles:
    qwen3-ollama-0_6b-1024:
      provider: ollama
      model: qwen3-embedding:0.6b
      dimensions: 1024
      max_input_tokens: 512
      batch_size: 16
  indexing:
    profile: qwen3-ollama-0_6b-1024
    chunk_tokens: 512
    overlap: 64
    batch_size: 16
  search:
    profile: qwen3-ollama-0_6b-1024
    top_k: 8
    hybrid: true
```

### Fields

| Field | Type | Default | Description |
|---|---|---|---|
| `cache_path` | string | `<cache-dir>/gitcode-mcp/cache.db` | SQLite cache database path |
| `lock_path` | string | `<cache_path>.lock` | Lock file path for writer ownership |
| `cache_mode` | string | `global` | Cache placement mode. Use `repo-local` to resolve the cache under the current Git worktree when no explicit cache path is set |
| `repository_docs` | object | `conventional-docs-v1` preset | Repository-owned documentation corpus intent. Valid only as committed policy in `.gitcode/gitcode-mcp.yaml`; see below. |
| `gitcode_base_url` | string | `https://api.gitcode.com/api/v5` | GitCode API base URL |
| `default_timeout` | duration | `30s` | Timeout for GitCode API calls and the CLI operation context |
| `max_response_size` | int64 | `10485760` | Maximum response size in bytes |
| `max_retries` | int | `2` | Maximum retries for API calls |
| `format` | string | `text` | Default output format (`text` or `json`) |
| `mcp.tools.access` | string | `write` | MCP discovery policy. `write` exposes read and write tools; `read` explicitly selects the read/status-only surface. Write calls still require `write_mode: "live"` and the normal readiness/audit gates. |
| `feedback.enabled` | bool | `false` | Enables submission to the trusted feedback sink. Preparation and `feedback status` remain available while disabled. |
| `feedback.sink` | string | `gitcode_issues` | Feedback destination adapter. The first supported sink is GitCode issues. |
| `service.job_retention.success_ttl` | duration | `48h` | TTL for succeeded and superseded jobs; bounded to 1 minute–90 days. |
| `service.job_retention.diagnostic_ttl` | duration | `336h` | TTL for failed, interrupted, and cancelled jobs; must be at least the success TTL and at most 365 days. |
| `service.job_retention.max_terminal_jobs` | int | `128` | Absolute terminal-history cap (1–4096); active jobs are never pruned. |
| `service.job_retention.max_diagnostic_jobs` | int | `32` | Separate hard bound for latest-failure-per-registration/work-stream preservation. |
| `service.job_retention.max_progress_events` | int | `256` | Per-job progress-event cap (1–4096). |
| `feedback.repo_id` | string | empty | Preconfigured repository binding used by the sink. Callers cannot override this destination. Required when feedback is enabled. |
| `feedback.labels` | pipe-separated string | empty | Labels attached to newly submitted reports, for example `feedback\|dogfood`. |
| `feedback.duplicate_policy` | string | `suggest` | Duplicate handling policy. `suggest` blocks on likely candidates for explicit review; `return_existing` selects the strongest likely match without writing. Exact fingerprint matches never create another issue. |
| `credential.store` | string | `auto` | Credential lookup mode: `auto` checks `GITCODE_TOKEN` then the system keyring, `env` checks only `GITCODE_TOKEN`, and `keyring` checks the system keyring after env fallback. `keychain` is accepted as a legacy alias for `keyring`. |
| `credential.keyring_service` | string | `gitcode-mcp` | System keyring service name used when `credential.store` is `auto` or `keyring`. Override it to isolate credentials for different agents or profiles. |
| `credential.keyring_account` | string | `token` | System keyring account/user name used when `credential.store` is `auto` or `keyring`. Override it to isolate credentials for different agents or profiles. |
| `rag.model_store_path` | string | `<cache-dir>/gitcode-mcp/models` | Global gitcode-mcp RAG model storage root. This is machine-level state and cannot be set from repo-local config. |
| `rag.default_profile` | string | `qwen3-ollama-0_6b-1024` | Default embedding/search profile. |
| `rag.providers.<name>.endpoint` | string | `http://127.0.0.1:11434` for `ollama` | Provider API endpoint. |
| `rag.providers.<name>.executable` | string | `ollama` for `ollama` | Provider executable used by managed startup. |
| `rag.providers.<name>.startup` | string | `managed` | Provider startup mode. Empty and `managed` allow `rag setup` to autostart the provider when `autostart` is true. |
| `rag.providers.<name>.autostart` | bool | `true` for `ollama` | Whether setup may start the provider runtime. |
| `rag.providers.<name>.env` | map | empty | Environment variables passed to managed provider startup, such as `OLLAMA_MODELS`. |
| `rag.providers.<name>.model_storage.env` | string | `OLLAMA_MODELS` for `ollama` | Provider-owned model path environment variable. |
| `rag.profiles.<name>.model` | string | `qwen3-embedding:0.6b` | Embedding model id. |
| `rag.profiles.<name>.dimensions` | int | `1024` | Expected embedding dimensions; mismatches are provider failures. |
| `rag.indexing.*` | mixed | profile `qwen3-ollama-0_6b-1024`, chunks `512`, overlap `64`, batch `16` | Indexing profile and chunking defaults. |
| `rag.search.*` | mixed | profile `qwen3-ollama-0_6b-1024`, top_k `8`, hybrid `true` | RAG search defaults. |

Environment overrides:

| Environment variable | Effect |
|---|---|
| `GITCODE_MCP_KEYRING_SERVICE` | Overrides `credential.keyring_service` for the launched process |
| `GITCODE_MCP_KEYRING_ACCOUNT` | Overrides `credential.keyring_account` for the launched process |
| `GITCODE_MCP_TOOL_ACCESS` | Overrides `mcp.tools.access`; set to `read` for an explicit read-only session |
| `GITCODE_MCP_RAG_PROFILE` | Overrides the default RAG profile for the launched process |
| `GITCODE_MCP_RAG_PROVIDER_ENDPOINT` | Overrides the active RAG provider endpoint |
| `GITCODE_MCP_RAG_MODEL_STORE` | Overrides `rag.model_store_path` |
| `GITCODE_MCP_SERVICE_RUNTIME_DIR` | Overrides `service.runtime_dir` |

## Repo-local cache mode

Repo-local mode keeps a repository-specific MCP cache under the current Git worktree:

```text
<git-worktree>/
  .gitcode/
    gitcode-mcp.yaml
    mcp/
      cache.db
      cache.db.lock
```

The same tracked file may declare versioned repository-document corpus intent:

```yaml
repository_docs:
  schema: 1
  enabled: true
  preset: conventional-docs-v1
  include:
    - runbooks/**/*.md
  exclude:
    - docs/generated/**
```

The default preset includes root `README*`, root `AGENTS.md`, and `docs/**`.
Use `preset: none` plus an explicit `include` list to replace it. Provider,
model, credential, cache-path, and machine resource settings do not belong in
this repository-owned section. See [Repository documentation
RAG](repository-documentation-rag.md) for revision, overlay, and storage rules.

Bootstrap it from inside the Git worktree:

```sh
gitcode-mcp repo init-local \
  --repo example-owner/example-repo \
  --owner example-owner \
  --name example-repo
```

The command creates or updates `.gitcode/gitcode-mcp.yaml`, ensures `.gitcode/mcp/` is ignored, creates the cache directory, and records the repository binding. It does not sync data.

The tracked config file contains:

```yaml
cache_mode: repo-local
```

The cache database and lock are local state and should stay ignored:

```gitignore
.gitcode/mcp/
```

When a command starts inside a worktree, `gitcode-mcp` walks up to the Git root and reads `.gitcode/gitcode-mcp.yaml`. Repo-local cache selection is opt-in and only applies when no command-line cache path, `GITCODE_MCP_CACHE_DIR`, or global config `cache_path` has already selected a cache. This keeps existing global-cache installs compatible while allowing Codex, Zed, and other repo-launched MCP clients to share the same per-repository cache without passing `--cache-path`.

`cache_mode: repo-local` is also accepted in the user config file. In that case the current Git worktree still supplies the repo root, and the cache resolves to `<git-worktree>/.gitcode/mcp/cache.db`.

Repo-local configs may tune RAG profiles, indexing, and search settings, but they cannot set machine-level storage paths such as `service.runtime_dir`, `rag.model_store_path`, or provider-owned model storage paths. Configure those globally or through environment variables.

## Inspecting configuration

### Show config location

```sh
gitcode-mcp config locate
```

Expected: prints the active config file path.

### Show config (redacted)

```sh
gitcode-mcp config show --redacted
```

Expected: prints effective configuration with token value replaced by `[REDACTED]`.
The output includes `cache_path_source`, `cache_mode`, credential keyring service/account, and, when repo-local discovery applies, `repo_root` and `repo_local_config_path`.

### Initialize config

```sh
gitcode-mcp config init
```

Creates the default config file if it does not already exist.

## Secrets

The GitCode API token is provided via the `GITCODE_TOKEN` environment variable or the configured system keyring. It is never stored in config files, logs, fixtures, or snapshots.

```sh
export GITCODE_TOKEN=<your-token>
```

See [Secrets](secrets.md) for platform-specific credential storage patterns.

## RAG

See [RAG Setup and Operation](rag.md) for provider installation, model storage, namespace invalidation, indexing, status, search, recovery, and optional real-model smoke tests.

Each provider should declare `data_boundary` as `local_process`, `local_network`, `remote`, or `unknown`. Maintenance planning reports that configured value and never infers data locality from an endpoint hostname.

## Structured feedback

The feedback sink is disabled by default. Configure it only in a trusted global configuration because it authorizes where `submit_feedback` may create issues. Neither MCP nor CLI accepts an arbitrary destination repository. See [Structured Feedback](feedback.md) for preparation, redaction, duplicate handling, and submission examples.

`gitcode-mcp feedback setup --repo OWNER/REPO` renders a path-free plan for an
already bound repository. Applying it requires `--yes`, the exact `--plan-id`,
and an idempotency key. The atomic update preserves unrelated YAML and comments,
uses private file permissions, and never stores a credential. Generic binaries
remain destination-neutral; trusted bundles may use this same global contract
to supply their feedback repository.

## Runtime audit

```sh
gitcode-mcp doctor --runtime-audit
```

Emits a JSON report with version, config path, config source, cache path/source/mode, repo-local discovery metadata, credential status, token presence, failure classes, and remediation hints.
