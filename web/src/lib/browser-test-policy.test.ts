import { describe, expect, it } from 'vitest';
import { resolveBrowserTestPolicy } from './browser-test-policy';

describe('browser CI artifact policy', () => {
  it('ignores every local visual input and disables trace in CI', () => {
    expect(resolveBrowserTestPolicy({
      CI: '1',
      ADMIN_QA_URL: 'http://127.0.0.1/private-launch',
      ADMIN_QA_OUTPUT: '/tmp/admin-qa',
      ADMIN_VIEW_QA_OUTPUT: '/tmp/operator-qa',
      ADMIN_QA_REFERENCE: '/tmp/reference.png',
      ADMIN_VISUAL_BASELINES: '1'
    })).toEqual({
      ci: true,
      trace: 'off',
      adminLaunchURL: undefined,
      adminQAOutput: undefined,
      operatorQAOutput: undefined,
      referencePath: undefined,
      visualBaselines: false
    });
  });

  it('keeps visual diagnostics an explicit local opt-in', () => {
    expect(resolveBrowserTestPolicy({
      ADMIN_QA_URL: 'http://127.0.0.1/local-launch',
      ADMIN_QA_OUTPUT: '/tmp/admin-qa',
      ADMIN_VIEW_QA_OUTPUT: '/tmp/operator-qa',
      ADMIN_QA_REFERENCE: '/tmp/reference.png',
      ADMIN_VISUAL_BASELINES: '1'
    })).toMatchObject({
      ci: false,
      trace: 'on-first-retry',
      adminLaunchURL: 'http://127.0.0.1/local-launch',
      adminQAOutput: '/tmp/admin-qa',
      operatorQAOutput: '/tmp/operator-qa',
      referencePath: '/tmp/reference.png',
      visualBaselines: true
    });
  });
});
