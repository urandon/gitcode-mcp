<script lang="ts">
  import type { CapabilityPolicy } from './admin';
  let { policy }: { policy: CapabilityPolicy } = $props();
</script>

<section class="policy" aria-label="Instance capability policy">
  <h2>Instance capability policy</h2>
  <p>Read-only operator policy. This browser cannot select a role or change grants.</p>
  <dl>
    <div><dt>Profile</dt><dd>{policy.profile || 'Instance ceiling'}</dd></div>
    <div><dt>Policy fingerprint</dt><dd><code>{policy.fingerprint}</code></dd></div>
    <div><dt>Authority classes</dt><dd>{policy.sources.join(', ') || 'Legacy (no managed policy)'}</dd></div>
  </dl>
  <details>
    <summary>Effective grants ({policy.decisions.filter((d) => d.allowed).length} allowed)</summary>
    <div class="grant-table"><table>
      <thead><tr><th scope="col">Capability</th><th scope="col">Decision</th><th scope="col">Reason</th></tr></thead>
      <tbody>{#each [...policy.decisions].sort((a, b) => a.id.localeCompare(b.id)) as decision (decision.id)}<tr><th scope="row"><code>{decision.id}</code></th><td>{decision.allowed ? 'Allowed' : 'Denied'}</td><td>{decision.reason}</td></tr>{/each}</tbody>
    </table></div>
  </details>
</section>

<style>
  .policy { border: 1px solid var(--border, #666); border-radius: 12px; padding: 1.25rem; margin-bottom: 1.5rem; }
  h2 { margin-top: 0; } p, dt { opacity: .8; } dl { display: grid; gap: .75rem; } dd { margin: .25rem 0 0; overflow-wrap: anywhere; }
  summary { cursor: pointer; } .grant-table { overflow-x: auto; } table { width: 100%; border-collapse: collapse; margin-top: 1rem; } th, td { text-align: left; padding: .5rem; border-bottom: 1px solid var(--border, #666); } th { font-weight: 500; }
</style>
