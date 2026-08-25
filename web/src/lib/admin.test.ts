import { describe, expect, it } from 'vitest';
import { cliHandoff, humanize, isSnapshotStale, laneSummary, statusTone } from './admin';

describe('admin presentation helpers', () => {
  it('keeps bounded tail language explicitly partial', () => {
    expect(laneSummary('tail', { state: 'partial', status: 'backfilling', stop_reason: 'max_pages' })).toBe('Partial · stopped by Max Pages');
    expect(laneSummary('tail', { state: 'current', status: 'complete' })).toContain('end-of-collection');
  });

  it('maps status and staleness without relying on color', () => {
    expect(statusTone('degraded')).toBe('warn');
    expect(humanize('retry_scheduled')).toBe('Retry Scheduled');
    expect(isSnapshotStale('2026-08-25T10:00:00Z', Date.parse('2026-08-25T10:06:00Z'))).toBe(true);
  });

  it('builds only fixed public-safe CLI handoffs', () => {
    expect(cliHandoff({ id: 'd', severity: 'warning', entity_type: 'cache', entity_id: 'cache-a', failure_class: 'cache_busy', message: 'busy', retryable: true, current: true })).toBe('gitcode-mcp service doctor');
    expect(cliHandoff({ id: 'm', severity: 'warning', entity_type: 'maintenance', entity_id: 'reg-a', failure_class: 'sync_failed', message: 'failed', retryable: true, current: true })).toBe('gitcode-mcp service maintenance --format json');
  });
});
