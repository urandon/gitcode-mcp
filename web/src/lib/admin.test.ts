import { describe, expect, it } from 'vitest';
import { cliHandoff, humanize, isSnapshotStale, jobNextAction, laneSummary, relativeAge, statusTone } from './admin';

describe('admin presentation helpers', () => {
  it('keeps bounded tail language explicitly partial', () => {
    expect(laneSummary('tail', { state: 'partial', status: 'backfilling', stop_reason: 'max_pages' })).toBe('Partial · stopped by Max Pages');
    expect(laneSummary('tail', { state: 'current', status: 'complete' })).toContain('end-of-collection');
  });

  it('maps status and staleness without relying on color', () => {
    expect(statusTone('degraded')).toBe('warn');
    expect(statusTone('partial/retrying')).toBe('warn');
    expect(statusTone('cancelling')).toBe('warn');
    expect(statusTone('cache_schema_blocked')).toBe('bad');
    expect(statusTone('credential_missing')).toBe('warn');
    expect(statusTone('provider_unavailable')).toBe('warn');
    expect(humanize('retry_scheduled')).toBe('Retry Scheduled');
    expect(humanize('partial/retrying')).toBe('Partial / Retrying');
    expect(isSnapshotStale('2026-08-25T10:00:00Z', Date.parse('2026-08-25T10:06:00Z'))).toBe(true);
  });

  it('renders deterministic relative age without discarding exact timestamps', () => {
    const now = Date.parse('2030-01-02T02:04:05Z');
    expect(relativeAge('2030-01-02T01:04:05Z', now)).toBe('1 hour ago');
    expect(relativeAge('2030-01-01T02:04:05Z', now)).toBe('1 day ago');
    expect(relativeAge('2030-01-02T02:34:05Z', now)).toBe('in 30 minutes');
    expect(relativeAge('invalid', now)).toBe('not recorded');
  });

  it('derives a typed list action without treating active backoff as retryable', () => {
    const now = Date.parse('2030-01-02T02:04:05Z');
    const failed = { status: 'failed', failure_class: 'provider_unavailable', failure_collection: 'wiki', retryable: true };
    expect(jobNextAction(failed, now)).toBe('Review Wiki and retry');
    expect(jobNextAction({ ...failed, retryable: false, retry_after: '2030-01-02T03:04:05Z' }, now)).toBe('Wait for scheduled retry in 1 hour');
    expect(jobNextAction({ ...failed, retryable: false, inspect_command: 'gitcode-mcp service job job-1 --format json' }, now)).toBe('Inspect retained failure');
  });

  it('builds only fixed public-safe CLI handoffs', () => {
    expect(cliHandoff({ id: 'd', severity: 'warning', entity_type: 'cache', entity_id: 'cache-a', failure_class: 'cache_busy', message: 'busy', retryable: true, current: true })).toBe('gitcode-mcp service doctor');
    expect(cliHandoff({ id: 'm', severity: 'warning', entity_type: 'maintenance', entity_id: 'reg-a', failure_class: 'sync_failed', message: 'failed', retryable: true, current: true })).toBe('gitcode-mcp service maintenance --format json');
  });
});
