# Dogfood Feedback

## Run Context

- Run ID: example-owner/example-repo
- Date: 2026-06-20
- Branch: feat/dogfood-iteration
- Token: gho_1a2b3c4d5e6f7g8h9i0j1k2l3m4n5o6p7q8r9s0t
- Authorization: Bearer xyz789abc123def456ghi789jkl012mno345pqr678stu901vwx234yz
- Cookie: session_id=abcdef1234567890abcdef1234567890; Path=/; HttpOnly
- Set-Cookie: auth_token=secret123; Domain=.example.com; Path=/
- Cache path: /Users/janedoe/gitcode-mcp/cache.db
- Fixture config: /home/urandon/.config/gitcode-fixtures.yaml

## Completed Checklist Slices

| Slice | Status | Transcript |
|-------|--------|------------|
| day1  | pass   | project/dogfood/evidence/day1.md |

## Observed Friction

### Friction #1

- Command / surface: gitcode-mcp sync --repo private-org/private-repo
- Host: internal.gitcode.corp.example.com
- Window: C:\Users\admin\gitcode-cache\cache.db
- Actual raw response: {"id":"ISSUE-99","title":"secret feature","body":"classified content","comments":[{"id":1,"body":"sensitive comment"}]}

## Follow-Up Owner

- Component(s): cache-sync
- Suggested task(s): Handle private repo sync timeouts
- Priority: high
