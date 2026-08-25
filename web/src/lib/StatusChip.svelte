<script lang="ts">
  import { statusTone } from './admin';

  let { value, label = '' }: { value: string; label?: string } = $props();
</script>

<span class="status-chip" class:good={statusTone(value) === 'good'} class:warn={statusTone(value) === 'warn'} class:bad={statusTone(value) === 'bad'}>
  <span class="status-dot" aria-hidden="true"></span>{label || value.replaceAll('_', ' ')}
</span>

<style>
  .status-chip { width: fit-content; display: inline-flex; align-items: center; gap: 7px; color: var(--muted); font-size: 12px; font-weight: 680; text-transform: capitalize; white-space: nowrap; }
  .status-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--subtle); }
  .good { color: var(--accent-dark); }
  .good .status-dot { background: var(--accent); }
  .warn { color: var(--warning-text); }
  .warn .status-dot { background: var(--warning); }
  .bad { color: var(--danger); }
  .bad .status-dot { background: var(--danger); }
</style>
