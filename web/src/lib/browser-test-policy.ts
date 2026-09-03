export type BrowserTestEnvironment = Record<string, string | undefined>;

export function resolveBrowserTestPolicy(env: BrowserTestEnvironment) {
  const ci = Boolean(env.CI);
  return {
    ci,
    trace: ci ? 'off' as const : 'on-first-retry' as const,
    screenshot: 'off' as const,
    video: 'off' as const,
    adminLaunchURL: ci ? undefined : env.ADMIN_QA_URL,
    adminQAOutput: ci ? undefined : env.ADMIN_QA_OUTPUT,
    operatorQAOutput: ci ? undefined : env.ADMIN_VIEW_QA_OUTPUT,
    referencePath: ci ? undefined : env.ADMIN_QA_REFERENCE,
    visualBaselines: !ci && env.ADMIN_VISUAL_BASELINES === '1'
  };
}
