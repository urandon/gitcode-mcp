import { expect, test } from '@playwright/test';
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

test('actual scheduled retry recovers the selected incident through native SSE without refresh', async ({ page, context }) => {
  test.setTimeout(120000);
  const directory = mkdtempSync(join(tmpdir(), 'gitcode-o5-active-'));
  let child: ChildProcessWithoutNullStreams | undefined;
  let lines: ReturnType<typeof createInterface> | undefined;
  try {
    const binary = join(directory, 'qualification.test');
    try { execFileSync('go', ['test', '-c', '-o', binary, './internal/qualification'], { cwd: resolve('..'), stdio: 'pipe', timeout: 60000 }); }
    catch { throw new Error('Offline active qualification fixture could not compile.'); }
    child = spawn(binary, ['-test.run=^TestO5ActualActiveRetryAndSSERecovery$', '-test.timeout=100s'], {
      cwd: resolve('..'), env: { ...process.env, TMPDIR: directory, GITCODE_MCP_O5_ACTIVE_BROWSER: '1' }, stdio: 'pipe'
    });
    child.stderr.resume();
    let origin: { url: string; job_ref: string; retry_after: string } | undefined;
    let done = false;
    let failed = false;
    let exited = false;
    let exitCode: number | null = null;
    const exit = new Promise<void>(resolve => child!.once('exit', code => { exited = true; exitCode = code; resolve(); }));
    child.once('error', () => { failed = true; });
    lines = createInterface({ input: child.stdout });
    lines.on('line', line => {
      if (line === 'O5_ACTIVE_DONE') { done = true; return; }
      if (!line.startsWith('O5_ACTIVE ')) return;
      try {
        const value = JSON.parse(line.slice('O5_ACTIVE '.length));
        if (origin || !/^http:\/\/127\.0\.0\.1:\d+$/.test(value.url) || !/^job-[a-f0-9]{32}$/.test(value.job_ref) || typeof value.retry_after !== 'string' || !Number.isFinite(Date.parse(value.retry_after))) { failed = true; return; }
        origin = value;
      } catch { failed = true; }
    });
    await expect.poll(() => Boolean(origin) || failed || exited, { timeout: 30000 }).toBe(true);
    if (!origin || failed || exited) throw new Error('Offline active qualification fixture protocol failed.');
    const authority = origin;
    const session = await context.request.post(authority.url + '/api/admin/v1/session', {
      headers: { Origin: authority.url }, data: { launch_token: 'qualification-launch' }
    });
    expect(session.status()).toBe(201);
    // Observe the native stream, never replace it or synthesize invalidation.
    await page.addInitScript(() => {
      (window as any).__activeSignals = 0;
      (window as any).__activeCopy = '';
      const NativeEventSource = window.EventSource;
      window.EventSource = class extends NativeEventSource {
        constructor(url: string | URL, options?: EventSourceInit) {
          super(url, options);
          this.addEventListener('observability_changed', () => { (window as any).__activeSignals++; });
        }
      };
      Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async (value: string) => { (window as any).__activeCopy = value; } } });
    });
    const methods: string[] = [];
    page.on('request', request => { if (request.url().startsWith(authority.url + '/api/')) methods.push(request.method()); });
    await page.goto(authority.url + '/?view=Service');
    const incidents = page.getByRole('region', { name: 'Active and recovered incidents', exact: true });
    await expect(incidents).toContainText('retrying');
    await expect(incidents).toContainText('Retry Wait');
    await expect(incidents).toContainText(new Date(authority.retry_after).toISOString());
    await incidents.getByRole('button', { name: 'Sync · Issues', exact: true }).click();
    const copy = page.getByRole('button', { name: 'Copy safe context', exact: true });
    // Bounded explicit observation retry is allowed only in the initial phase.
    for (let attempt = 0; attempt < 5; attempt++) {
      const refresh = page.getByRole('button', { name: 'Refresh observation', exact: true });
      await expect(refresh).toBeEnabled({ timeout: 3000 });
      if (await copy.isEnabled()) break;
      await refresh.click();
    }
    await expect(copy).toBeEnabled();
    await copy.click();
    const initial = JSON.parse(await page.evaluate(() => (window as any).__activeCopy));
    expect(initial.execution_state).toBe('degraded');
    expect(initial.incident.state).toBe('retrying');
    expect(initial.incident.reason).toBe('retry_wait');
    expect(initial.incident.job_ref).toBe(authority.job_ref);
    expect(initial.incident.retry_after).toBe(authority.retry_after);
    expect(initial.incident.last_success_at).toBeTruthy();
    expect(Date.parse(authority.retry_after)).toBeGreaterThan(Date.now());
    await expect.poll(() => page.evaluate(() => (window as any).__activeSignals), { timeout: 3000 }).toBeGreaterThan(0);
    const signalCount = await page.evaluate(() => (window as any).__activeSignals);
    // No navigation, selection change or Refresh after the active snapshot.
    await expect.poll(() => done || failed || exited, { timeout: 45000 }).toBe(true);
    if (!done || failed || exited) throw new Error('Actual scheduled retry did not reach its recovery boundary.');
    await expect.poll(() => page.evaluate(() => (window as any).__activeSignals), { timeout: 5000 }).toBeGreaterThan(signalCount);
    await expect(incidents).toContainText('recovered', { timeout: 5000 });
    try { await expect(copy).toBeEnabled({ timeout: 3000 }); }
    catch {
      // Finite semantic flags only: never print provider bodies or coordinates.
      const flags = {
        reading: await page.getByRole('button', { name: 'Refreshing observation…', exact: true }).count() > 0,
        busy: await page.getByRole('alert').filter({ hasText: 'busy' }).count() > 0,
        errors: await page.getByRole('alert').count(),
        frozen: await page.getByRole('status').filter({ hasText: 'Continuation stopped at a discontinuity' }).count() > 0,
        selectionMissing: await page.getByRole('status').filter({ hasText: 'Selection not retained' }).count() > 0
      };
      throw new Error('Recovered context unavailable: ' + JSON.stringify(flags));
    }
    await copy.click();
    const recovered = JSON.parse(await page.evaluate(() => (window as any).__activeCopy));
    expect(recovered.execution_state).toBe('healthy');
    expect(recovered.boot_id).toBe(initial.boot_id);
    expect(recovered.incident.incident_ref).toBe(initial.incident.incident_ref);
    expect(recovered.incident.job_ref).toBe(authority.job_ref);
    expect(recovered.incident.state).toBe('recovered');
    expect(recovered.incident.reason).toBe('retry_wait');
    expect(recovered.incident.first_observed_at).toBe(initial.incident.first_observed_at);
    expect(recovered.incident.retry_after).toBeUndefined();
    expect(Date.parse(recovered.incident.last_success_at)).toBeGreaterThan(Date.parse(initial.incident.last_success_at));
    await expect(incidents).toContainText(new Date(recovered.incident.last_success_at).toISOString());
    for (const value of [initial, recovered]) {
      expect(Buffer.byteLength(JSON.stringify(value), 'utf8')).toBeLessThanOrEqual(16 * 1024);
      expect(JSON.stringify(value)).not.toContain('synthetic-private');
    }
    expect(await page.locator('body').innerText()).not.toContain('synthetic-private');
    expect(methods.every(method => method === 'GET')).toBe(true);
    child.stdin.write('continue\n');
    await bounded(exit, 5000, 'Active qualification fixture did not join.');
    expect(exitCode).toBe(0);
  } finally {
    lines?.close();
    if (child && child.pid && child.exitCode === null && child.signalCode === null) {
      const stopped = new Promise<void>(resolve => child!.once('exit', () => resolve()));
      child.kill('SIGTERM');
      try { await bounded(stopped, 2000, 'Active qualification fixture did not stop.'); }
      catch { child.kill('SIGKILL'); await bounded(stopped, 2000, 'Active qualification fixture did not join.'); }
    }
    rmSync(directory, { recursive: true, force: true });
  }
});
