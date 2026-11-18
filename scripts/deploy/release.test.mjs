import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const script = fileURLToPath(new URL('./release-channel.sh', import.meta.url));

function channel(tag) {
  const result = spawnSync('bash', [script, tag], { encoding: 'utf8' });
  return { ...result, output: `${result.stdout}${result.stderr}` };
}

test('正式版本更新 latest', () => {
  for (const tag of ['v0.1.0', 'v1.2.3', 'v10.20.30']) {
    const result = channel(tag);
    assert.equal(result.status, 0, result.output);
    assert.match(result.stdout, /^prerelease=false$/m);
    assert.match(result.stdout, /^release_flag=--latest$/m);
  }
});

test('预发布标签只创建预发布，不更新 latest', () => {
  for (const tag of ['v0.2.0-rc.1', 'v0.2.0-beta', 'v0.2.0-alpha.2']) {
    const result = channel(tag);
    assert.equal(result.status, 0, result.output);
    assert.match(result.stdout, /^prerelease=true$/m);
    assert.match(result.stdout, /^release_flag=--prerelease$/m);
    assert.doesNotMatch(result.stdout, /--latest/);
  }
});

test('拒绝无法识别或缺少版本的标签', () => {
  for (const tag of ['', 'v0.2', 'v0.2.0.1', '0.2.0', 'v0.2.0-', 'v0.2.0+build', 'release-0.2.0']) {
    const result = channel(tag);
    assert.equal(result.status, 2, `${tag}: ${result.output}`);
    assert.match(result.output, /无效的发布标签/);
  }
});
