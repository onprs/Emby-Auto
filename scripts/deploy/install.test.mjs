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
  for (const binary of ['api', 'worker']) writeFileSync(join(payload, 'bin', `emby-auto-${binary}`), '#!/bin/sh\nexit 0\n');
  writeFileSync(join(payload, 'web/index.html'), '<!doctype html><title>Emby Auto</title>');
  for (const name of ['emby-auto-api.service', 'emby-auto-worker.service', 'nginx.conf']) {
    copyFileSync(join(root, 'deploy/direct', name), join(payload, 'config', name));
  }
  copyFileSync(join(root, 'deploy/nginx/locations.conf'), join(payload, 'config/emby-auto-locations.conf'));
  shell('find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS', payload);
  const sandbox = join(directory, 'sandbox');
  mkdirSync(join(sandbox, 'etc/nginx/conf.d'), { recursive: true });
  mkdirSync(join(sandbox, 'etc/systemd/system'), { recursive: true });

  function archive(extraArgs = []) {
    const result = spawnSync('tar', [...extraArgs, '-czf', join(assets, archiveName), '-C', assets, 'emby-auto-direct-amd64'], { encoding: 'utf8' });
    assert.equal(result.status, 0, result.stderr);
    const digest = createHash('sha256').update(readFileSync(join(assets, archiveName))).digest('hex');
    writeFileSync(join(assets, 'SHA256SUMS'), `${digest}  ${archiveName}\n`);
  }
  archive();

  // 所有写入均指向临时目录；有宿主副作用的命令用函数替换。
  const harness = `
source "$INSTALLER_PATH"
install_dir="$TEST_ROOT/opt/emby-auto"
config_dir="$TEST_ROOT/etc/emby-auto"
state_dir="$TEST_ROOT/var/lib/emby-auto"
storage_dir="$TEST_ROOT/srv/emby-auto"
unit_dir="$TEST_ROOT/etc/systemd/system"
nginx_dir="$TEST_ROOT/etc/nginx"
postgres_dir="$TEST_ROOT/etc/postgresql/17/emby_auto"
lock_dir="$TEST_ROOT/run/emby-auto-install"
check_platform() { target_arch=amd64; distribution_codename=bookworm; }
require_root() { :; }
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
apt-cache() { return 0; }
ss() { if [[ "\${BUSY_PORT:-}" != '' && "$*" == *":$BUSY_PORT" ]]; then printf 'LISTEN 0 128\\n'; fi; }
nginx() { printf '%s\\n' "$*" >>"$TEST_ROOT/nginx.log"; }
systemctl() {
  printf '%s\\n' "$*" >>"$TEST_ROOT/service.log"
  if [[ "\${WORKER_FAIL:-0}" == 1 && "$*" == "is-active --quiet emby-auto-worker.service" ]]; then return 3; fi
  if [[ "\${DATABASE_FAIL:-0}" == 1 && "$*" == "is-active --quiet postgresql@17-emby_auto.service" ]]; then return 3; fi
}
pg_createcluster() { mkdir -p "$postgres_dir"; touch "$postgres_dir/postgresql.conf"; printf '%s\\n' "$*" >>"$TEST_ROOT/cluster.log"; }
pg_conftool() { printf '%s\\n' "$*" >>"$TEST_ROOT/cluster.log"; }
runuser() {
  local actor="$2"
  shift 3
  if [[ "\${BLOCK_MEDIA:-0}" == 1 && "$actor" == emby-auto && "\${*: -1}" == "$storage_dir/media/anime" ]]; then return 1; fi
  if [[ "$1" == psql ]]; then
    printf '%s\\n' "$*" >>"$TEST_ROOT/database-arguments.log"
    command tee "$TEST_ROOT/database-input.log" >/dev/null
  else
    "$@"
  fi
}
fetch() {
  [[ "\${FETCH_FAIL:-0}" != 1 ]] || return 22
  cp "$TEST_ASSETS/\${1##*/}" "$2"
}
curl() {
  [[ "\${HEALTH_FAIL:-0}" != 1 ]] || return 22
  printf '{"state":"required","databaseManagedExternally":true}\\n' >"\${*: -1}"
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

function setting(directory, key) {
  return readFileSync(join(directory, 'etc/emby-auto/installer.env'), 'utf8').split('\n').find((line) => line.startsWith(`${key}=`)).slice(key.length + 1);
}

test('文件入口与管道入口均可在普通账户显示帮助', () => {
  for (const [args, input] of [[[installer, '--help'], undefined], [['-s', '--', '--help'], readFileSync(installer, 'utf8')]]) {
    const result = spawnSync('bash', args, { input, encoding: 'utf8' });
    assert.equal(result.status, 0, result.stdout + result.stderr);
    assert.match(result.stdout, /无需 Docker/);
    assert.match(result.stdout, /--database-port/);
  }
});

test('完整安装创建独立数据库与服务，并使用用户指定的端口', (t) => {
  const f = fixture(t);
  const result = f.run(['--version', 'v1.2.3', '--port', '19080', '--api-port', '19081', '--database-port', '19082'], { CREATE_ACCOUNT: '1' });
  assert.equal(result.status, 0, result.output);
  assert.match(result.stdout, /http:\/\/127.0.0.1:19080/);
  assert.ok(existsSync(join(f.sandbox, 'etc/emby-auto/installed')));
  const environment = readFileSync(join(f.sandbox, 'etc/emby-auto/runtime.env'), 'utf8');
  assert.match(environment, /API_ADDRESS=127\.0\.0\.1:19081/);
  assert.match(environment, /@127\.0\.0\.1:19082\/emby_auto\?sslmode=disable/);
  assert.match(environment, /DATABASE_MANAGED_EXTERNALLY=true/);
  assert.match(readFileSync(join(f.sandbox, 'cluster.log'), 'utf8'), /--port 19082 --start-conf=auto 17 emby_auto/);
  assert.match(readFileSync(join(f.sandbox, 'cluster.log'), 'utf8'), /--encoding=UTF8 --locale=C\.UTF-8/);
  assert.match(readFileSync(join(f.sandbox, 'cluster.log'), 'utf8'), /listen_addresses 127\.0\.0\.1/);
  assert.match(readFileSync(join(f.sandbox, 'etc/nginx/conf.d/emby-auto.conf'), 'utf8'), /listen 127\.0\.0\.1:19080/);
  assert.match(readFileSync(join(f.sandbox, 'etc/nginx/conf.d/emby-auto.conf'), 'utf8'), /server 127\.0\.0\.1:19081/);
  assert.match(readFileSync(join(f.sandbox, 'etc/systemd/system/emby-auto-api.service.d/installer.conf'), 'utf8'), /User=emby-auto/);
  assert.match(readFileSync(join(f.sandbox, 'service.log'), 'utf8'), /enable --now emby-auto-api.service emby-auto-worker.service nginx.service/);
  assert.match(readFileSync(join(f.sandbox, 'useradd.log'), 'utf8'), /--system --user-group/);
  assert.equal(statSync(join(f.sandbox, 'etc/emby-auto/runtime.env')).mode & 0o777, 0o640);
  assert.equal(statSync(join(f.sandbox, 'etc/emby-auto/installer.env')).mode & 0o777, 0o600);
  assert.equal(statSync(join(f.sandbox, 'opt/emby-auto/current/bin/emby-auto-api')).mode & 0o777, 0o755);
  const password = setting(f.sandbox, 'database_password');
  assert.match(password, /^[a-f0-9]{64}$/);
  assert.ok(!result.output.includes(password));
  assert.ok(!readFileSync(join(f.sandbox, 'database-arguments.log'), 'utf8').includes(password));
  assert.match(readFileSync(join(f.sandbox, 'database-input.log'), 'utf8'), /CREATE DATABASE emby_auto OWNER emby_auto/);
});

test('PostgreSQL 服务实例解码后指向实际创建的集群配置', (t) => {
  const f = fixture(t);
  const result = f.run();
  assert.equal(result.status, 0, result.output);
  const services = readFileSync(join(f.sandbox, 'service.log'), 'utf8').split('\n');
  const unit = services.find((line) => line.startsWith('enable --now postgresql@')).split(' ').at(-1);
  const instance = unit.slice('postgresql@'.length, -'.service'.length);
  const decoded = spawnSync('systemd-escape', ['--unescape', instance], { encoding: 'utf8' });
  assert.equal(decoded.status, 0, decoded.stderr);
  assert.ok(existsSync(join(f.sandbox, 'etc/postgresql', decoded.stdout.trim(), 'postgresql.conf')),
    'systemd 的 AssertPathExists 必须能找到数据库集群配置');
});

test('数据库启动未成功时停止安装并保留凭据供重试', (t) => {
  const f = fixture(t);
  let result = f.run([], { DATABASE_FAIL: '1' });
  assert.notEqual(result.status, 0);
  assert.match(result.output, /PostgreSQL 实例启动失败/);
  assert.ok(!existsSync(join(f.sandbox, 'database-input.log')));
  assert.ok(!existsSync(join(f.sandbox, 'etc/emby-auto/installed')));
  const password = setting(f.sandbox, 'database_password');
  result = f.run();
  assert.equal(result.status, 0, result.output);
  assert.equal(setting(f.sandbox, 'database_password'), password);
});

test('重复安装保留版本、凭据和用户调整后的运行配置', (t) => {
  const f = fixture(t);
  let result = f.run(['--version', 'v1.2.3']);
  assert.equal(result.status, 0, result.output);
  const path = join(f.sandbox, 'etc/emby-auto/runtime.env');
  const environment = readFileSync(path, 'utf8').replace('LOG_LEVEL=info', 'LOG_LEVEL=debug');
  writeFileSync(path, environment);
  const password = setting(f.sandbox, 'database_password');
  rmSync(f.assets, { recursive: true });
  result = f.run();
  assert.equal(result.status, 0, result.output);
  assert.equal(readFileSync(path, 'utf8'), environment);
  assert.equal(setting(f.sandbox, 'database_password'), password);
  result = f.run(['--version', 'v2.0.0']);
  assert.notEqual(result.status, 0);
  assert.match(result.output, /按部署文档升级/);
});

test('启动失败停止应用；再次执行保留原数据库凭据并完成安装', (t) => {
  const f = fixture(t);
  let result = f.run([], { HEALTH_FAIL: '1' });
  assert.notEqual(result.status, 0);
  assert.match(result.output, /服务未通过启动检查/);
  assert.match(readFileSync(join(f.sandbox, 'service.log'), 'utf8'), /stop emby-auto-worker.service emby-auto-api.service/);
  assert.ok(!existsSync(join(f.sandbox, 'etc/emby-auto/installed')));
  const password = setting(f.sandbox, 'database_password');
  result = f.run();
  assert.equal(result.status, 0, result.output);
  assert.equal(setting(f.sandbox, 'database_password'), password);
  assert.ok(existsSync(join(f.sandbox, 'etc/emby-auto/installed')));
});

test('API 可访问但 Worker 未启动时拒绝完成安装', (t) => {
  const f = fixture(t);
  const result = f.run([], { WORKER_FAIL: '1' });
  assert.notEqual(result.status, 0);
  assert.match(result.output, /服务未通过启动检查/);
  assert.ok(!existsSync(join(f.sandbox, 'etc/emby-auto/installed')));
  assert.match(readFileSync(join(f.sandbox, 'service.log'), 'utf8'), /stop emby-auto-worker.service emby-auto-api.service/);
});

test('下载失败和发布包校验失败均发生在安装依赖与写入配置之前', (t) => {
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

test('已有手动部署会在下载和修改服务之前退出', (t) => {
  const f = fixture(t);
  mkdirSync(join(f.sandbox, 'etc/emby-auto'), { recursive: true });
  const path = join(f.sandbox, 'etc/emby-auto/runtime.env');
  writeFileSync(path, 'DATABASE_URL=preserve-existing-database\n');
  const result = f.run();
  assert.notEqual(result.status, 0);
  assert.match(result.output, /已有手动部署/);
  assert.equal(readFileSync(path, 'utf8'), 'DATABASE_URL=preserve-existing-database\n');
  assert.ok(!existsSync(join(f.sandbox, 'packages.log')));
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
  assert.ok(!readFileSync(join(f.sandbox, 'chown.log'), 'utf8').includes(directory));
});

test('目录权限失败后可改用媒体账户重试，不遗留错误账户的应用目录', (t) => {
  const f = fixture(t);
  mkdirSync(join(f.sandbox, 'srv/emby-auto/media/anime'), { recursive: true });
  let result = f.run([], { BLOCK_MEDIA: '1' });
  assert.notEqual(result.status, 0);
  assert.match(result.output, /--user/);
  assert.ok(!existsSync(join(f.sandbox, 'var/lib/emby-auto')));
  assert.ok(!existsSync(join(f.sandbox, 'srv/emby-auto/downloads')));
  result = f.run(['--user', 'media-owner'], { BLOCK_MEDIA: '1' });
  assert.equal(result.status, 0, result.output);
  assert.equal(setting(f.sandbox, 'service_user'), 'media-owner');
});

test('非法参数、冲突端口和 root 账户在安装前被拒绝', (t) => {
  for (const args of [
    ['--port', '65536'], ['--port', '1023'], ['--port', '18081'], ['--port', '1;touch'],
    ['--user', 'root'], ['--user', 'missing-user'], ['--user', '../account'], ['--version', '../../release'], ['--port'],
  ]) {
    const f = fixture(t);
    const result = f.run(args);
    assert.notEqual(result.status, 0, args.join(' '));
    assert.ok(!existsSync(join(f.sandbox, 'packages.log')));
    assert.ok(!existsSync(join(f.sandbox, 'etc/emby-auto')));
  }
  const f = fixture(t);
  const result = f.run([], { BUSY_PORT: '18080' });
  assert.notEqual(result.status, 0);
  assert.match(result.output, /端口 18080 已被占用/);
  assert.ok(!existsSync(join(f.sandbox, 'packages.log')));
});
