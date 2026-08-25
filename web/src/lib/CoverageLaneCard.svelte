<script lang="ts">
  import { CircleDashed, Gauge, History, Layers3, MessageSquareMore, Search } from '@lucide/svelte';
  import type { CoverageLane } from './admin';
  import { humanize, laneSummary } from './admin';
  import StatusChip from './StatusChip.svelte';

  let { name, lane, compact = false }: { name: string; lane: CoverageLane; compact?: boolean } = $props();
  const icons: Record<string, typeof Gauge> = { head: Gauge, tail: History, secondary: MessageSquareMore, projection: Layers3, rag: Search };
  let Icon = $derived(icons[name] || CircleDashed);
</script>

<article class="lane" class:compact aria-label={`${humanize(name)} coverage: ${laneSummary(name, lane)}`}>
  <div class="lane-heading">
    <span class="lane-icon"><Icon size={16} /></span>
    <div><strong>{humanize(name)}</strong><StatusChip value={lane.state} /></div>
  </div>
  <p>{laneSummary(name, lane)}</p>
  {#if !compact}
    <dl>
      {#if lane.records_listed}<div><dt>Listed</dt><dd>{lane.records_listed.toLocaleString()}</dd></div>{/if}
      {#if lane.pages_listed}<div><dt>Pages</dt><dd>{lane.pages_listed.toLocaleString()}</dd></div>{/if}
      {#if lane.eligible}<div><dt>Eligible</dt><dd>{lane.eligible.toLocaleString()}</dd></div>{/if}
      {#if lane.embedded}<div><dt>Embedded</dt><dd>{lane.embedded.toLocaleString()}</dd></div>{/if}
      {#if lane.updated_at}<div><dt>Observed</dt><dd>{new Date(lane.updated_at).toLocaleString()}</dd></div>{/if}
    </dl>
  {/if}
</article>

<style>
  .lane { min-width: 0; padding: 18px; border: 1px solid var(--line); border-radius: 9px; background: var(--surface); box-shadow: var(--shadow); }
  .lane-heading { display: flex; align-items: flex-start; gap: 12px; }
  .lane-heading > div { min-width: 0; display: grid; gap: 6px; }
  .lane-heading strong { font-size: 14px; }
  .lane-icon { width: 32px; height: 32px; display: grid; place-items: center; flex: none; border-radius: 8px; background: var(--accent-soft); color: var(--accent-dark); }
  p { min-height: 38px; margin: 14px 0 0; color: var(--muted); font-size: 12px; line-height: 1.5; }
  dl { margin: 15px 0 0; padding-top: 12px; border-top: 1px solid var(--line-soft); display: grid; gap: 7px; }
  dl div { display: flex; justify-content: space-between; gap: 12px; font-size: 11px; }
  dt { color: var(--muted); }
  dd { margin: 0; overflow: hidden; color: var(--text); font: 600 11px ui-monospace, SFMono-Regular, Menlo, monospace; text-overflow: ellipsis; white-space: nowrap; }
  .compact { padding: 14px; box-shadow: none; }
  .compact p { min-height: 0; margin-top: 10px; }
</style>
