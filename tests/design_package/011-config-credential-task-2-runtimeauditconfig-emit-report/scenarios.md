# RuntimeAuditConfig emit report validation scenarios

## 011-config-credential-task-2-runtimeauditconfig-emit-report-scenario-1

Run the product CLI command `gitcode-mcp doctor --runtime-audit --repo fixture-repo` offline through the production CLI entrypoint with a temporary explicit YAML config source and an environment token.

The runtime audit output must include the config-owned section and fields:

- installed version
- active config path, source, format, and existence
- cache path and cache-path source
- credential source and credential store mode
- token-present status without token value
- config/auth failure class field
- typed handoff fields for resolved config and cache paths

The composed text command must not synthesize cache, repo, MCP, or index success; those owner sections must be absent or explicitly `not_reported_by_owner`, never `ok`.

## 011-config-credential-task-2-runtimeauditconfig-emit-report-scenario-2

Run fixture-backed CLI runtime-audit checks through the production command route for these offline states:

- valid YAML config with token present
- missing config with missing token
- malformed YAML config containing a file-secret sentinel
- legacy JSON config from `GITCODE_CONFIG`
- missing token with otherwise valid YAML
- keyring-unavailable state via the product's runtime audit reporter seam

For every state, verify the config subsection and typed handoff fields are present. Verify config/auth failure classes are typed (`no-config`, `config-malformed`, `legacy-config`, `token-missing`, `credential-store-unavailable`) as applicable. Verify cache/repo/MCP/index success is not synthesized by this component. Verify no token value, config-file secret, or raw credential-store diagnostic is emitted while sanitized temporary paths remain visible.
