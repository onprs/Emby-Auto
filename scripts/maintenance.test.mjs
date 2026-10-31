import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtemp, mkdir, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { test } from 'node:test';

const artifacts = {
  'backend/internal/transport/httpapi/api.gen.go': 'package httpapi\n',
  'backend/db/sqlc/models.go': 'package db\n',
  'apps/web/src/api/generated/types.gen.ts': 'export type Resource = { id: string };\n',
};

const fakeGo = `#!/usr/bin/env node
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
const args = process.argv.slice(2);
if (process.env.MAINTENANCE_INSPECT_GO) {
  console.log(JSON.stringify({ args, toolchain: process.env.GOTOOLCHAIN, work: process.env.GOWORK, cwd: process.cwd() }));
  process.exit(0);
}
const tool = args[2];
if (tool === 'deadcode') {
  if (process.env.MAINTENANCE_DEADCODE_STATUS) process.exit(2);
  process.stdout.write(process.env.MAINTENANCE_DEADCODE_REPORT ?? '');
  process.exit(0);
}
if (process.env.MAINTENANCE_GENERATOR_FAILURE) process.exit(3);
const option = (name) => args[args.indexOf(name) + 1];
let output;
let content;
if (tool === 'oapi-codegen') {
  const config = await readFile(option('--config'), 'utf8');
  if (!config.includes('package: httpapi')) throw new Error('生成选项丢失');
  output = config.match(/^output: (.+)$/m)[1];
  content = 'package httpapi\\n';
} else if (tool === 'sqlc') {
  const configRoot = path.dirname(option('--file'));
  await readFile(path.join(configRoot, 'db/query/example.sql'));
  await readFile(path.join(configRoot, 'db/migrations/000001_schema.up.sql'));
  output = path.join(configRoot, 'db/sqlc/models.go');
  content = 'package db\\n';
} else {
  throw new Error('未知工具参数：' + args.join(' '));
}
await mkdir(path.dirname(output), { recursive: true });
await writeFile(output, content);
`;

const fakeNpm = `#!/usr/bin/env node
import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
const args = process.argv.slice(2);
if (args[0] !== 'run' || args[1] !== 'generate:sdk' || !args.includes('--output')) {
  throw new Error('SDK 生成参数无效');
}
const output = args[args.indexOf('--output') + 1];
await mkdir(output, { recursive: true });
await writeFile(path.join(output, 'types.gen.ts'), 'export type Resource = { id: string };\\n');
`;

async function put(root, name, content, mode) {
  const destination = path.join(root, name);
  await mkdir(path.dirname(destination), { recursive: true });
  await writeFile(destination, content, { mode });
}

async function fixture(t) {
  const root = await mkdtemp(path.join(tmpdir(), 'emby-auto-maintenance '));
  t.after(() => rm(root, { recursive: true, force: true }));
  for (const name of ['check-generated.sh', 'check-deadcode.sh', 'dev/go-tool.sh']) {
    await put(root, `scripts/${name}`, await readFile(new URL(name, import.meta.url), 'utf8'));
  }
  await put(root, 'tools/go.mod', 'module maintenance.test/tools\n\ngo 1.27.3\n');
  await put(root, 'contracts/oapi-codegen.yaml', 'package: httpapi\noutput: internal/transport/httpapi/api.gen.go\n');
  await put(root, 'contracts/openapi.yaml', 'openapi: 3.0.3\n');
  await put(root, 'backend/sqlc.yaml', 'version: "2"\n');
  await put(root, 'backend/db/query/example.sql', '-- name: Example :one\nSELECT 1;\n');
  await put(root, 'backend/db/migrations/000001_schema.up.sql', 'SELECT 1;\n');
  for (const [name, content] of Object.entries(artifacts)) await put(root, name, content);
  await put(root, 'bin/go', fakeGo, 0o755);
  await put(root, 'bin/npm', fakeNpm, 0o755);
  await mkdir(path.join(root, 'tmp'));
  const env = { ...process.env, PATH: `${path.join(root, 'bin')}${path.delimiter}${process.env.PATH}`, TMPDIR: path.join(root, 'tmp') };
  delete env.GOTOOLCHAIN;
  return { root, env };
}

function run(f, script, args = [], env = {}) {
  return spawnSync('bash', [path.join(f.root, 'scripts', script), ...args], {
    cwd: f.root,
    env: { ...f.env, ...env },
    encoding: 'utf8',
    timeout: 10_000,
  });
}

async function snapshot(root, prefix = '') {
  const result = {};
  for (const entry of await readdir(path.join(root, prefix), { withFileTypes: true })) {
    if (prefix === '' && entry.name === 'tmp') continue;
    const name = path.join(prefix, entry.name);
    if (entry.isDirectory()) Object.assign(result, await snapshot(root, name));
    else result[name] = await readFile(path.join(root, name), 'utf8');
  }
  return result;
}

async function checkWithoutChanges(f, expectedStatus) {
  const before = await snapshot(f.root);
  const result = run(f, 'check-generated.sh');
  assert.equal(result.status, expectedStatus, result.stderr || result.stdout);
  assert.deepEqual(await snapshot(f.root), before, '检查不能改写工作区文件');
  assert.deepEqual(await readdir(path.join(f.root, 'tmp')), [], '检查结束后应清理临时目录');
  return result;
}

test('生成结果一致时检查通过，包含空格的路径保持可用', async (t) => {
  await checkWithoutChanges(await fixture(t), 0);
});

for (const name of Object.keys(artifacts)) {
  test(`发现生成差异且保留原文件：${name}`, async (t) => {
    const f = await fixture(t);
    await put(f.root, name, '需要重新生成\n');
    await checkWithoutChanges(f, 1);
  });
}

test('发现不再由生成器产生的陈旧文件', async (t) => {
  const f = await fixture(t);
  await put(f.root, 'backend/db/sqlc/obsolete.go', 'package db\n');
  const result = await checkWithoutChanges(f, 1);
  assert.match(result.stdout, /obsolete\.go/);
});

test('缺少生成文件时给出可执行的修复命令', async (t) => {
  const f = await fixture(t);
  await rm(path.join(f.root, 'backend/internal/transport/httpapi/api.gen.go'));
  const result = await checkWithoutChanges(f, 1);
  assert.match(result.stderr, /npm run generate/);
});

test('无法隔离 OpenAPI 输出时停止生成', async (t) => {
  const f = await fixture(t);
  await put(f.root, 'contracts/oapi-codegen.yaml', 'package: httpapi\n');
  const result = await checkWithoutChanges(f, 1);
  assert.match(result.stderr, /无法定位 OpenAPI 输出配置/);
});

test('生成器失败时保留工作区并清理临时目录', async (t) => {
  const f = await fixture(t);
  f.env.MAINTENANCE_GENERATOR_FAILURE = '1';
  await checkWithoutChanges(f, 3);
});

test('开发工具默认跟随工具模块声明的 Go 版本', async (t) => {
  const f = await fixture(t);
  const result = run(f, 'dev/go-tool.sh', ['sqlc', 'generate'], { MAINTENANCE_INSPECT_GO: '1' });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(JSON.parse(result.stdout), {
    args: ['tool', '-modfile=../tools/go.mod', 'sqlc', 'generate'],
    toolchain: 'go1.27.3',
    work: 'off',
    cwd: path.join(f.root, 'backend'),
  });
});

test('开发工具保留显式指定的 Go 工具链', async (t) => {
  const f = await fixture(t);
  const result = run(f, 'dev/go-tool.sh', ['sqlc', 'generate'], { MAINTENANCE_INSPECT_GO: '1', GOTOOLCHAIN: 'local' });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(JSON.parse(result.stdout).toolchain, 'local');
});

test('迁移工具保留 PostgreSQL 构建标签和完整参数', async (t) => {
  const f = await fixture(t);
  const result = run(f, 'dev/go-tool.sh', ['migrate', '-path', 'db/migrations', 'up'], { MAINTENANCE_INSPECT_GO: '1' });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(JSON.parse(result.stdout).args, [
    'run', '-modfile=../tools/go.mod', '-tags=postgres',
    'github.com/golang-migrate/migrate/v4/cmd/migrate', '-path', 'db/migrations', 'up',
  ]);
});

test('工具入口缺少工具名时报告用法', async (t) => {
  const result = run(await fixture(t), 'dev/go-tool.sh');
  assert.equal(result.status, 2);
  assert.match(result.stderr, /usage:/);
});

test('死代码检查将诊断结果转为失败退出码', async (t) => {
  const f = await fixture(t);
  const success = run(f, 'check-deadcode.sh');
  assert.equal(success.status, 0, success.stderr);
  const finding = run(f, 'check-deadcode.sh', [], { MAINTENANCE_DEADCODE_REPORT: 'model.go:1:1: unreachable func: unused\n' });
  assert.equal(finding.status, 1);
  assert.match(finding.stderr, /unreachable func/);
  const failure = run(f, 'check-deadcode.sh', [], { MAINTENANCE_DEADCODE_STATUS: '1' });
  assert.equal(failure.status, 2);
});
