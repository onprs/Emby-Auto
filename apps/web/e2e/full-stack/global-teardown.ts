import { spawnSync } from 'node:child_process';
import path from 'node:path';

export default function globalTeardown() {
  const script = path.resolve(process.cwd(), '../../scripts/e2e/cleanup-full-stack.sh');
  const result = spawnSync('bash', [script], { stdio: 'inherit' });
  if (result.error) {
    throw result.error;
  }
  if (result.status !== 0) {
    throw new Error(`full-stack cleanup exited with status ${result.status}`);
  }
}
