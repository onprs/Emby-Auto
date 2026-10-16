import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir, userInfo } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const installer = join(root, 'install.sh');
const archiveName = 'emby-auto-direct-amd64-linux-amd64.tar.gz';

function shell(command, cwd, args = []) {
  const result = spawnSync('bash', ['-c', command, 'installer-test', ...args], { cwd, encoding: 'utf8' });
  assert.equal(result.status, 0, result.stdout + result.stderr);
  return result.stdout;
}

function fixture(t) {
  const directory = mkdtempSync(join(tmpdir(), 'emby-auto-install-test-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const assets = join(directory, 'assets');
  const payload = join(assets, 'emby-auto-direct-amd64');
  for (const part of ['bin', 'web', 'config']) mkdirSync(join(payload, part), { recursive: true });
  for (const binary of ['api', 'worker', 'migration-check', 'background-runtime', 'release-preflight', 'recover-stale-operations', 'migrate-runtime-addresses', 'migrate-staging-ownership']) {
    writeFileSync(join(payload, 'bin', `emby-auto-${binary}`), '#!/bin/sh\nexit 0\n');
  }
  writeFileSync(join(payload, 'bin/emby-auto-host-control'), ['#!/bin/sh', 'echo running', ''].join('\n'));
  writeFileSync(join(payload, 'web/index.html'), '<!doctype html><title>Emby Auto</title>');
  for (const name of ['emby-auto-api.service', 'emby-auto-worker.service', 'emby-auto-systemd-control.service', 'worker-runtime.sh']) {
    copyFileSync(join(root, 'deploy/direct', name), join(payload, 'config', name));
  }
  shell('find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS', payload);
  const sandbox = join(directory, 'sandbox');
  mkdirSync(join(sandbox, 'etc/systemd/system'), { recursive: true });

  function archive(extraArgs = []) {
    const result = spawnSync('tar', [...extraArgs, '-czf', join(assets, archiveName), '-C', assets, 'emby-auto-direct-amd64'], { encoding: 'utf8' });
    assert.equal(result.status, 0, result.stderr);
    const digest = createHash('sha256').update(readFileSync(join(assets, archiveName))).digest('hex');
    writeFileSync(join(assets, 'SHA256SUMS'), `${digest}  ${archiveName}\n`);
  }
  archive();

  // 所有写入均指向临时目录；系统操作替换为记录调用的函数。
  const harness = `
source "$INSTALLER_PATH"
install_dir="$TEST_ROOT/opt/emby-auto"
config_dir="$TEST_ROOT/etc/emby-auto"
state_dir="$TEST_ROOT/var/lib/emby-auto"
storage_dir="$TEST_ROOT/srv/emby-auto"
unit_dir="$TEST_ROOT/etc/systemd/system"
lock_dir="$TEST_ROOT/run/emby-auto-install"
check_platform() { target_arch=amd64; }
require_root() { :; }
command() {
  if [[ "$1" == -v && ( "$2" == ffmpeg || "$2" == ffprobe ) ]]; then
    [[ "\${REUSE_MEDIA_TOOLS:-0}" == 1 ]] && return 0
    return 1
  fi
  builtin command "$@"
}
id() {
  if [[ "\${*: -1}" == root ]]; then command id "$@"; return; fi
  [[ "\${*: -1}" != missing-user ]] || return 1
  if [[ "\${CREATE_ACCOUNT:-0}" == 1 && ! -f "$TEST_ROOT/account" ]]; then return 1; fi
  case "$1" in
    -u) printf '%s\\n' "$TEST_UID" ;;
    -g) printf '%s\\n' "$TEST_GID" ;;
  esac
}
useradd() { touch "$TEST_ROOT/account"; printf '%s\\n' "$*" >>"$TEST_ROOT/useradd.log"; }
install() {
  local args=()
  while (( $# )); do
    case "$1" in -o|-g) shift 2 ;; *) args+=("$1"); shift ;; esac
  done
  command install "\${args[@]}"
}
chown() { printf '%s\\n' "$*" >>"$TEST_ROOT/chown.log"; }
apt-get() { printf '%s\\n' "$*" >>"$TEST_ROOT/packages.log"; }
ss() { if [[ "\${BUSY_PORT:-}" != '' && "$*" == *":$BUSY_PORT" ]]; then printf 'LISTEN 0 128\\n'; fi; }
systemctl() {
  printf '%s\\n' "$*" >>"$TEST_ROOT/service.log"
  if [[ "\${WORKER_FAIL:-0}" == 1 && "$*" == "is-active --quiet emby-auto-worker.service" ]]; then return 3; fi
}
forbidden() { printf '%s\\n' "$*" >>"$TEST_ROOT/forbidden.log"; return 97; }
nginx() { forbidden nginx "$@"; }
pg_createcluster() { forbidden pg_createcluster "$@"; }
pg_conftool() { forbidden pg_conftool "$@"; }
psql() { forbidden psql "$@"; }
runuser() {
  local actor="$2"
  shift 3
  if [[ "\${BLOCK_MEDIA:-0}" == 1 && "$actor" == emby-auto && "\${*: -1}" == "$storage_dir/media/anime" ]]; then return 1; fi
  "$@"
}
fetch() {
  [[ "\${FETCH_FAIL:-0}" != 1 ]] || return 22
  cp "$TEST_ASSETS/\${1##*/}" "$2"
}
curl() {
  [[ "\${HEALTH_FAIL:-0}" != 1 ]] || return 22
  if [[ "$*" == *'/api/v1/setup/status'* ]]; then
    printf '{"state":"required","databaseConfigured":false,"databaseManagedExternally":false}\\n' >"\${*: -1}"
  else
    printf '<!doctype html><title>Emby Auto</title>\\n' >"\${*: -1}"
  fi
}
sleep() { :; }
main "$@"
`;

  function run(args = [], env = {}) {
    const result = spawnSync('bash', ['-c', harness, 'installer-test', ...args], {
      encoding: 'utf8',
      env: { ...process.env, INSTALLER_PATH: installer, TEST_ROOT: sandbox, TEST_ASSETS: assets,
        TEST_UID: String(userInfo().uid || 1000), TEST_GID: String(userInfo().gid || 1000), ...env },
    });
    return { ...result, output: result.stdout + result.stderr };
  }
  return { sandbox, assets, payload, archive, run };
}

test('文件入口与管道入口均可在普通账户显示帮助', () => {
  for (const [args, input] of [[[installer, '--help'], undefined], [['-s', '--', '--help'], readFileSync(installer, 'utf8')]]) {
    const result = spawnSync('bash', args, { input, encoding: 'utf8' });
    assert.equal(result.status, 0, result.stdout + result.stderr);
    assert.match(result.stdout, /无需 Docker/);
    assert.match(result.stdout, /自行准备 PostgreSQL/);
    assert.doesNotMatch(result.stdout, /--database-port|--api-port/);
  }
});

test('完整安装仅配置应用和 FFmpeg，网页与 API 共用指定端口', (t) => {
  const f = fixture(t);
  const result = f.run(['--version', 'v1.2.3', '--port', '19080'], { CREATE_ACCOUNT: '1' });
  assert.equal(result.status, 0, result.output);
  assert.match(result.stdout, /http:\/\/127.0.0.1:19080/);
  assert.match(result.stdout, /初始化向导中填写 PostgreSQL/);
  assert.ok(existsSync(join(f.sandbox, 'etc/emby-auto/installed')));
  const environment = readFileSync(join(f.sandbox, 'etc/emby-auto/runtime.env'), 'utf8');
  assert.match(environment, /^API_ADDRESS=127\.0\.0\.1:19080$/m);
  assert.match(environment, /^WEB_ROOT=.*\/opt\/emby-auto\/current\/web$/m);
  assert.doesNotMatch(environment, /DATABASE_URL|DATABASE_MANAGED_EXTERNALLY|CONFIG_ENCRYPTION_KEY/);
  assert.match(environment, /^RIVER_GENERAL_WORKERS=10$/m);
  assert.match(environment, /^RIVER_TRANSCODE_WORKERS=1$/m);
  assert.match(readFileSync(join(f.sandbox, 'etc/systemd/system/emby-auto-api.service.d/installer.conf'), 'utf8'), /User=emby-auto/);
  assert.match(readFileSync(join(f.sandbox, 'service.log'), 'utf8'), /enable --now emby-auto-systemd-control.service emby-auto-api.service emby-auto-worker.service/);
  assert.doesNotMatch(readFileSync(join(f.sandbox, 'service.log'), 'utf8'), /postgresql|nginx/);
  assert.equal(readFileSync(join(f.sandbox, 'packages.log'), 'utf8'), 'update\ninstall -y --no-install-recommends ca-certificates ffmpeg\n');
  assert.match(readFileSync(join(f.sandbox, 'useradd.log'), 'utf8'), /--system --user-group/);
  assert.equal(statSync(join(f.sandbox, 'etc/emby-auto/runtime.env')).mode & 0o777, 0o640);
  assert.equal(statSync(join(f.sandbox, 'etc/emby-auto/installer.env')).mode & 0o777, 0o600);
  assert.equal(statSync(join(f.sandbox, 'opt/emby-auto/current/bin/emby-auto-api')).mode & 0o777, 0o755);
  assert.match(readFileSync(join(f.sandbox, 'etc/emby-auto/installer.env'), 'utf8'), /^installer_format=3$/m);
  assert.doesNotMatch(readFileSync(join(f.sandbox, 'etc/emby-auto/installer.env'), 'utf8'), /database_password/);
  assert.ok(!existsSync(join(f.sandbox, 'var/lib/emby-auto/bootstrap.json')));
  assert.ok(!existsSync(join(f.sandbox, 'forbidden.log')));
});

test('用户已有数据库与 Web 服务配置在安装后保持原样', (t) => {
  const f = fixture(t);
  const files = ['etc/nginx/conf.d/emby-auto.conf', 'etc/nginx/emby-auto-locations.conf', 'etc/postgresql/17/emby_auto/postgresql.conf'];
  for (const file of files) {
    mkdirSync(dirname(join(f.sandbox, file)), { recursive: true });
    writeFileSync(join(f.sandbox, file), 'user-managed configuration\n');
  }
  const result = f.run();
  assert.equal(result.status, 0, result.output);
  for (const file of files) assert.equal(readFileSync(join(f.sandbox, file), 'utf8'), 'user-managed configuration\n');
  assert.ok(!existsSync(join(f.sandbox, 'forbidden.log')));
});

test('重复安装保留版本、初始化状态和用户调整后的配置', (t) => {
  const f = fixture(t);
  let result = f.run(['--version', 'v1.2.3']);
  assert.equal(result.status, 0, result.output);
  const path = join(f.sandbox, 'etc/emby-auto/runtime.env');
  const environment = readFileSync(path, 'utf8').replace('LOG_LEVEL=info', 'LOG_LEVEL=debug');
  writeFileSync(path, environment);
  const bootstrap = join(f.sandbox, 'var/lib/emby-auto/bootstrap.json');
  writeFileSync(bootstrap, 'preserve completed initialization\n');
  const record = readFileSync(join(f.sandbox, 'etc/emby-auto/installer.env'), 'utf8');
  rmSync(f.assets, { recursive: true });
  result = f.run();
  assert.equal(result.status, 0, result.output);
  assert.equal(readFileSync(path, 'utf8'), environment);
  assert.equal(readFileSync(bootstrap, 'utf8'), 'preserve completed initialization\n');
  assert.equal(readFileSync(join(f.sandbox, 'etc/emby-auto/installer.env'), 'utf8'), record);
  result = f.run(['--version', 'v2.0.0']);
  assert.notEqual(result.status, 0);
  assert.match(result.output, /按部署文档升级/);
});

test('旧版安装记录在下载、安装依赖和修改服务之前被拒绝', (t) => {
  const f = fixture(t);
  mkdirSync(join(f.sandbox, 'etc/emby-auto'), { recursive: true });
  const path = join(f.sandbox, 'etc/emby-auto/installer.env');
  const original = 'service_user=emby-auto\nweb_port=18080\napi_port=18081\ndatabase_port=18082\nversion=latest\n';
  writeFileSync(path, original);
  const result = f.run();
  assert.notEqual(result.status, 0);
  assert.match(result.output, /安装记录版本不兼容/);
  assert.equal(readFileSync(path, 'utf8'), original);
  assert.ok(!existsSync(join(f.sandbox, 'packages.log')));
  assert.ok(!existsSync(join(f.sandbox, 'service.log')));
});

test('启动失败停止应用；再次执行保留配置并完成安装', (t) => {
  const f = fixture(t);
  let result = f.run([], { HEALTH_FAIL: '1' });
  assert.notEqual(result.status, 0);
  assert.match(result.output, /服务未通过启动检查/);
  assert.match(readFileSync(join(f.sandbox, 'service.log'), 'utf8'), /stop emby-auto-worker.service emby-auto-api.service/);
  assert.ok(!existsSync(join(f.sandbox, 'etc/emby-auto/installed')));
  const environment = readFileSync(join(f.sandbox, 'etc/emby-auto/runtime.env'), 'utf8');
  result = f.run();
  assert.equal(result.status, 0, result.output);
  assert.equal(readFileSync(join(f.sandbox, 'etc/emby-auto/runtime.env'), 'utf8'), environment);
});

test('API 可访问但 Worker 未启动时拒绝完成安装', (t) => {
  const f = fixture(t);
  const result = f.run([], { WORKER_FAIL: '1' });
  assert.notEqual(result.status, 0);
  assert.match(result.output, /服务未通过启动检查/);
  assert.ok(!existsSync(join(f.sandbox, 'etc/emby-auto/installed')));
});

test('下载失败和校验失败均发生在安装依赖与写入配置之前', (t) => {
  const f = fixture(t);
  let result = f.run([], { FETCH_FAIL: '1' });
  assert.notEqual(result.status, 0);
  assert.match(result.output, /无法下载发布包/);
  writeFileSync(join(f.assets, archiveName), 'corrupted release');
  result = f.run();
  assert.notEqual(result.status, 0);
  assert.match(result.output, /SHA-256 校验失败/);
  assert.ok(!existsSync(join(f.sandbox, 'packages.log')));
  assert.ok(!existsSync(join(f.sandbox, 'etc/emby-auto')));
});

test('拒绝内容不匹配、路径越界和符号链接的发布包', (t) => {
  for (const kind of ['content', 'traversal', 'symlink']) {
    const f = fixture(t);
    if (kind === 'content') writeFileSync(join(f.payload, 'web/index.html'), 'modified');
    if (kind === 'symlink') symlinkSync('/etc/passwd', join(f.payload, 'web/link'));
    f.archive(kind === 'traversal' ? ['--transform=s#web/index.html#../escape#'] : []);
    const result = f.run();
    assert.notEqual(result.status, 0, kind);
    assert.match(result.output, /内容校验失败|无效路径|不支持的链接/);
    assert.ok(!existsSync(join(f.sandbox, 'packages.log')));
  }
});

test('已有手动部署在下载和修改服务之前退出', (t) => {
  const f = fixture(t);
  mkdirSync(join(f.sandbox, 'etc/emby-auto'), { recursive: true });
  const path = join(f.sandbox, 'etc/emby-auto/runtime.env');
  writeFileSync(path, 'DATABASE_URL=preserve-existing-database\n');
  const result = f.run();
  assert.notEqual(result.status, 0);
  assert.match(result.output, /已有手动部署/);
  assert.equal(readFileSync(path, 'utf8'), 'DATABASE_URL=preserve-existing-database\n');
  assert.ok(!existsSync(join(f.sandbox, 'service.log')));
});

test('已有媒体文件和目录权限在安装后保持不变', (t) => {
  const f = fixture(t);
  const directory = join(f.sandbox, 'srv/emby-auto/media/anime');
  mkdirSync(directory, { recursive: true, mode: 0o750 });
  writeFileSync(join(directory, 'preserve.txt'), 'existing media');
  const before = statSync(directory);
  const result = f.run(['--user', 'media-owner']);
  assert.equal(result.status, 0, result.output);
  assert.equal(readFileSync(join(directory, 'preserve.txt'), 'utf8'), 'existing media');
  const after = statSync(directory);
  assert.equal(after.mode, before.mode);
  assert.equal(after.uid, before.uid);
  assert.equal(after.gid, before.gid);
});

test('权限不足、缺失账户、root 账户和占用端口在服务修改前被拒绝', (t) => {
  for (const [args, env, message] of [
    [[], { BUSY_PORT: '18080' }, /端口 18080 已被占用/],
    [['--user', 'missing-user'], {}, /请先创建指定的媒体账户/],
    [['--user', 'root'], {}, /UID 和 GID 必须大于 0/],
    [[], { BLOCK_MEDIA: '1' }, /无法读写/],
  ]) {
    const f = fixture(t);
    if (env.BLOCK_MEDIA) mkdirSync(join(f.sandbox, 'srv/emby-auto/media/anime'), { recursive: true });
    const result = f.run(args, env);
    assert.notEqual(result.status, 0);
    assert.match(result.output, message);
    assert.ok(!existsSync(join(f.sandbox, 'service.log')));
  }
});

test('本地制品安装校验完整发布包，并且不会访问下载入口', (t) => {
  const f = fixture(t);
  const result = f.run(['--archive', join(f.assets, archiveName), '--checksums', join(f.assets, 'SHA256SUMS')], { FETCH_FAIL: '1' });
  assert.equal(result.status, 0, result.output);
  assert.ok(existsSync(join(f.sandbox, 'etc/emby-auto/host-control.env')));
  assert.equal(statSync(join(f.sandbox, 'etc/emby-auto/host-control.env')).mode & 0o777, 0o600);
});

test('迁移准备不启动服务，后续启动保留准备好的制品和配置', (t) => {
  const f = fixture(t);
  let result = f.run(['--no-start']);
  assert.equal(result.status, 0, result.output);
  assert.ok(existsSync(join(f.sandbox, 'etc/emby-auto/prepared')));
  assert.ok(!existsSync(join(f.sandbox, 'etc/emby-auto/installed')));
  assert.doesNotMatch(readFileSync(join(f.sandbox, 'service.log'), 'utf8'), /enable|start /);
  const environment = readFileSync(join(f.sandbox, 'etc/emby-auto/runtime.env'), 'utf8');
  rmSync(f.assets, { recursive: true });
  result = f.run([], { FETCH_FAIL: '1' });
  assert.equal(result.status, 0, result.output);
  assert.equal(readFileSync(join(f.sandbox, 'etc/emby-auto/runtime.env'), 'utf8'), environment);
});

test('旧版原生安装记录在下载和修改服务之前被拒绝', (t) => {
  const f = fixture(t);
  mkdirSync(join(f.sandbox, 'etc/emby-auto'), { recursive: true });
  const path = join(f.sandbox, 'etc/emby-auto/installer.env');
  const original = 'installer_format=2\nservice_user=emby-auto\nweb_port=18080\nversion=latest\n';
  writeFileSync(path, original);
  const result = f.run();
  assert.notEqual(result.status, 0);
  assert.match(result.output, /安装记录版本不兼容/);
  assert.equal(readFileSync(path, 'utf8'), original);
  assert.ok(!existsSync(join(f.sandbox, 'packages.log')));
  assert.ok(!existsSync(join(f.sandbox, 'service.log')));
});

test('拒绝符号链接安装目录并保留目标内容', (t) => {
  const f = fixture(t);
  mkdirSync(join(f.sandbox, 'opt'), { recursive: true });
  symlinkSync(f.assets, join(f.sandbox, 'opt/emby-auto'));
  const archive = readFileSync(join(f.assets, archiveName));
  const result = f.run();
  assert.notEqual(result.status, 0);
  assert.match(result.output, /安装目录不能是符号链接/);
  assert.deepEqual(readFileSync(join(f.assets, archiveName)), archive);
  assert.ok(!existsSync(join(f.sandbox, 'service.log')));
});

test('准备后的制品发生变化时拒绝启动并保留当前内容', (t) => {
  const f = fixture(t);
  let result = f.run(['--no-start']);
  assert.equal(result.status, 0, result.output);
  const path = join(f.sandbox, 'opt/emby-auto/current/bin/emby-auto-api');
  writeFileSync(path, 'changed-after-preparation');
  rmSync(join(f.sandbox, 'service.log'));
  result = f.run();
  assert.notEqual(result.status, 0);
  assert.match(result.output, /内容已变化/);
  assert.equal(readFileSync(path, 'utf8'), 'changed-after-preparation');
  assert.ok(!existsSync(join(f.sandbox, 'service.log')));
  assert.ok(!existsSync(join(f.sandbox, 'etc/emby-auto/installed')));
});

test('迁移完成登记只验证服务，不重新启动或改写配置', (t) => {
  const f = fixture(t);
  let result = f.run(['--no-start']);
  assert.equal(result.status, 0, result.output);
  const path = join(f.sandbox, 'etc/emby-auto/runtime.env');
  const environment = readFileSync(path, 'utf8');
  rmSync(f.assets, { recursive: true });
  rmSync(join(f.sandbox, 'service.log'));
  result = f.run(['--finalize'], { FETCH_FAIL: '1' });
  assert.equal(result.status, 0, result.output);
  assert.ok(existsSync(join(f.sandbox, 'etc/emby-auto/installed')));
  assert.equal(readFileSync(path, 'utf8'), environment);
  assert.doesNotMatch(readFileSync(join(f.sandbox, 'service.log'), 'utf8'), /enable|start |stop |daemon-reload/);
});

test('迁移完成登记失败保持既有服务与配置', (t) => {
  const f = fixture(t);
  let result = f.run(['--no-start']);
  assert.equal(result.status, 0, result.output);
  const path = join(f.sandbox, 'etc/emby-auto/runtime.env');
  const environment = readFileSync(path, 'utf8');
  rmSync(join(f.sandbox, 'service.log'));
  result = f.run(['--finalize'], { HEALTH_FAIL: '1' });
  assert.notEqual(result.status, 0);
  assert.ok(!existsSync(join(f.sandbox, 'etc/emby-auto/installed')));
  assert.equal(readFileSync(path, 'utf8'), environment);
  assert.ok(!existsSync(join(f.sandbox, 'service.log')));
});

test('已有 FFmpeg 与 ffprobe 时复用工具并保留系统软件包', (t) => {
  const f = fixture(t);
  const result = f.run([], { REUSE_MEDIA_TOOLS: '1' });
  assert.equal(result.status, 0, result.output);
  assert.ok(!existsSync(join(f.sandbox, 'packages.log')));
});

test('无效端口及旧版端口选项在写入前被拒绝', (t) => {
  for (const args of [['--port', '80'], ['--port', '99999'], ['--port', '018080'], ['--api-port', '18081'], ['--database-port', '18082']]) {
    const f = fixture(t);
    const result = f.run(args);
    assert.notEqual(result.status, 0);
    assert.ok(!existsSync(join(f.sandbox, 'packages.log')));
    assert.ok(!existsSync(join(f.sandbox, 'service.log')));
  }
});
