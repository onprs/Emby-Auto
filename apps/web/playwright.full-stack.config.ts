import { existsSync } from 'node:fs';

import { defineConfig, devices, type Project } from '@playwright/test';

const edgePaths = [
  'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
  'C:\\Program Files\\Microsoft\\Edge\\Application\\msedge.exe',
];
const apiPort = process.env.E2E_API_PORT ?? '18081';
const optionalProjects: Project[] = edgePaths.some(existsSync)
  ? [{ name: 'edge', grep: /@cross-browser/, use: { ...devices['Desktop Edge'], channel: 'msedge' } }]
  : [];

export default defineConfig({
  testDir: './e2e/full-stack',
  timeout: 120_000,
  expect: { timeout: 30_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: 'list',
  globalTeardown: './e2e/full-stack/global-teardown.ts',
  use: {
    baseURL: 'http://127.0.0.1:5174',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    { name: 'chromium', grep: /@(cross-browser|recovery|movie)/, use: { ...devices['Desktop Chrome'] } },
    { name: 'firefox', grep: /@cross-browser/, use: { ...devices['Desktop Firefox'] } },
    ...optionalProjects,
    { name: 'narrow-chromium', grep: /@narrow/, use: { ...devices['Desktop Chrome'], viewport: { width: 390, height: 844 } } },
  ],
  webServer: [
    {
      command: 'bash ../../scripts/e2e/start-full-stack.sh',
      url: `http://127.0.0.1:${apiPort}/api/v1/health/live`,
      reuseExistingServer: false,
      timeout: 180_000,
    },
    {
      command: 'vite --port 5174',
      url: 'http://127.0.0.1:5174',
      reuseExistingServer: false,
      timeout: 60_000,
      env: { VITE_API_PROXY_TARGET: `http://127.0.0.1:${apiPort}` },
    },
  ],
});
