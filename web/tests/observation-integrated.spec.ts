import { expect, test, type Page } from '@playwright/test';
import { execFileSync, spawn, type ChildProcessWithoutNullStreams } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { createInterface } from 'node:readline';

async function bounded<T>(promise: Promise<T>, milliseconds: number, message: string): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([promise, new Promise<never>((_, reject) => {
      timer = setTimeout(() => reject(new Error(message)), milliseconds);
    })]);
  } finally { clearTimeout(timer); }
}

async function readyContext(page: Page): Promise<void> {
  const refresh = page.getByRole('button', { name: 'Refresh observation', exact: true });
  const copy = page.getByRole('button', { name: 'Copy safe context', exact: true });
  for (let attempt = 0; attempt < 5; attempt++) {
    await expect(refresh).toBeEnabled({ timeout: 3000 });
    if (await copy.isEnabled()) break;
    await refresh.click();
  }
  await expect(copy).toBeEnabled({ timeout: 3000 });
}

// The test-only Go binary runs real HTTP sync, SQLite/job ownership, IPC and
// authenticated Admin. Browser responses and EventSource are not mocked.
test('actual failed job survives durable reload and recovers without observation side effects', async ({ page, context }) => {
  test.setTimeout(120000);
  const directory = mkdtempSync(join(tmpdir(), 'gitcode-o5-'));
  const binary = join(directory, 'qualification.test');
  let child: ChildProcessWithoutNullStreams | undefined;
  let lines: ReturnType<typeof createInterface> | undefined;
  try {
    try { execFileSync('go', ['test', '-c', '-o', binary, './internal/qualification'], { cwd: resolve('..'), stdio: 'pipe', timeout: 60000 }); }
    catch { throw new Error('Offline qualification fixture could not compile.'); }
    child = spawn(binary, ['-test.run=^TestO5ActualFailureWriterWaitRecoveryAcrossTransports$/^retained$', '-test.timeout=100s'], {
      cwd: resolve('..'), env: { ...process.env, TMPDIR: directory, GITCODE_MCP_O5_BROWSER: '1' }, stdio: 'pipe'
    });
    // Never echo arbitrary fixture output or local coordinates into reports.
    child.stderr.resume();
    const origins: Array<{ url: string; state: string; job_ref: string }> = [];
    lines = createInterface({ input: child.stdout });
    let finished = false;
    let exitCode: number | null = null;
    let protocolFailed = false;
    const exited = new Promise<void>(done => child!.once('exit', code => { finished = true; exitCode = code; done(); }));
    child.once('error', () => { protocolFailed = true; finished = true; });
    lines.on('line', line => {
      if (!line.startsWith('O5_BROWSER ')) return;
      try {
        const value = JSON.parse(line.slice('O5_BROWSER '.length));
        if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(value.url) || !['degraded', 'healthy'].includes(value.state) || !/^job-[a-f0-9]{32}$/.test(value.job_ref)) { protocolFailed = true; return; }
        origins.push(value);
      } catch { protocolFailed = true; }
    });
    await page.addInitScript(() => {
      (window as any).__copiedO5 = '';
      Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async (value: string) => { (window as any).__copiedO5 = value; } } });
    });
    let priorBoot = '';
    for (let phase = 0; phase < 3; phase++) {
      await expect.poll(() => origins.length > phase || finished || protocolFailed, { timeout: 30000 }).toBe(true);
      if (protocolFailed) throw new Error('Offline qualification fixture protocol failed.');
      if (origins.length <= phase) throw new Error('Actual qualification fixture stopped before its browser boundary.');
      const origin = origins[phase];
      const session = await context.request.post(origin.url + '/api/admin/v1/session', {
        headers: { Origin: origin.url }, data: { launch_token: 'qualification-launch' }
      });
      expect(session.status()).toBe(201);
      const snapshot = await context.request.get(origin.url + '/api/admin/v1/observability');
      expect(snapshot.status()).toBe(200);
      const summary = await snapshot.json();
      expect(summary.execution_health.state).toBe(phase < 2 ? 'degraded' : 'healthy');
      if (phase === 0) priorBoot = summary.collector.boot_id;
      if (phase === 2) expect(summary.collector.boot_id).not.toBe(priorBoot);
      await page.goto(origin.url + '/?view=Service');
      await expect(page.getByRole('heading', { name: 'Service', exact: true })).toBeVisible();
      // Parallel read-only snapshots may return observation_busy by contract.
      // Exercise the explicit bounded read retry, not an execution retry.
      const copyButton = page.getByRole('button', { name: 'Copy safe context', exact: true });
      await readyContext(page);
      await expect(page.getByRole('heading', { name: 'Execution and observation' })).toBeVisible();
      await expect(page.getByRole('heading', { name: 'Event timeline' })).toBeVisible();
      const incident = summary.execution_health.incidents.find((value: any) => value.collection === 'issues');
      expect(incident).toBeDefined();
      expect(incident.last_success_at).toBeTruthy();
      const incidentSection = page.getByRole('region', { name: 'Active and recovered incidents', exact: true });
      const incidentButton = incidentSection.getByRole('button', { name: 'Sync · Issues', exact: true });
      await incidentButton.click();
      await expect(page.getByRole('region', { name: 'Selected observation', exact: true })).toBeVisible();
      await readyContext(page);
      await page.getByRole('button', { name: 'Copy safe context', exact: true }).click();
      const copy = JSON.parse(await page.evaluate(() => (window as any).__copiedO5));
      expect(copy.execution_state).toBe(summary.execution_health.state);
      expect(copy.boot_id).toBe(summary.collector.boot_id);
      expect(copy.incident).toEqual(incident);
      expect(copy.incident.state).toBe(phase < 2 ? 'active' : 'recovered');
      await expect(incidentSection).toContainText(incident.reason.split('_').map((value: string) => value[0].toUpperCase() + value.slice(1)).join(' '));
      await expect(incidentSection).toContainText(new Date(incident.last_success_at).toISOString());
      expect(Buffer.byteLength(JSON.stringify(copy), 'utf8')).toBeLessThanOrEqual(16 * 1024);
      expect(await page.locator('body').innerText()).not.toContain('synthetic-private');
      expect(JSON.stringify(copy)).not.toContain('synthetic-private');
      // The recovery phase must select its actual new job, not fall back to
      // the older succeeded sibling or the original failed incident job.
      expect(origin.job_ref === incident.job_ref).toBe(phase < 2);
      await page.goto(origin.url + '/?view=Service&obs_code=job_terminal&obs_job_ref=' + origin.job_ref);
      const terminal = page.getByRole('list', { name: 'Retained catalog events' }).getByRole('button', { name: 'Job Terminal', exact: true });
      await readyContext(page);
      await expect(terminal).toHaveCount(1);
      await terminal.click();
      await expect(page.getByRole('region', { name: 'Selected observation', exact: true })).toContainText('Job Terminal');
      await readyContext(page);
      await copyButton.click();
      const eventCopy = JSON.parse(await page.evaluate(() => (window as any).__copiedO5));
      expect(eventCopy.event.job_ref).toBe(origin.job_ref);
      expect(eventCopy.event.boot_id).toBe(summary.collector.boot_id);
      expect(eventCopy.event.repo_ref).toBe(incident.repo_ref);
      expect(eventCopy.event.cache_ref).toBe(incident.cache_ref);
      expect(eventCopy.event.code).toBe('job_terminal');
      expect(eventCopy.event.phase).toBe('completion');
      expect(eventCopy.event.outcome).toBe(phase < 2 ? 'failed' : 'succeeded');
      expect(eventCopy.event.failure_class).toBe(phase < 2 ? 'operation_failed' : undefined); // #191; not provider-specific qualification.
      expect(eventCopy.event.correlation_ref).toBeTruthy();
      expect(Buffer.byteLength(JSON.stringify(eventCopy), 'utf8')).toBeLessThanOrEqual(16 * 1024);
      expect(JSON.stringify(eventCopy)).not.toContain('synthetic-private');
      await page.getByRole('button', { name: 'Refresh observation', exact: true }).click();
      await expect(page.getByRole('heading', { name: 'Service', exact: true })).toBeVisible();
      child.stdin.write('continue\n');
    }
    await bounded(exited, 5000, 'Offline qualification fixture did not finish.');
    expect(exitCode).toBe(0);
  } finally {
    lines?.close();
    if (child && child.pid && child.exitCode === null && child.signalCode === null) {
      const stopped = new Promise<void>(done => child!.once('exit', () => done()));
      child.kill('SIGTERM');
      try { await bounded(stopped, 2000, 'Offline qualification fixture did not stop.'); }
      catch { child.kill('SIGKILL'); await bounded(stopped, 2000, 'Offline qualification fixture did not join.'); }
    }
    rmSync(directory, { recursive: true, force: true });
  }
});
