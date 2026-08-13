import { Cpu, HardDrive, MemoryStick, Network, RefreshCw } from 'lucide-react';
import {
  CartesianGrid,
  Legend,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';

import type { SystemDiskUsage, SystemMetricSample, SystemMetricsSnapshot } from '@/api/generated/types.gen';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { formatBytes, formatDateTime } from '@/lib/format';

type SystemResourceChartsProps = {
  metrics?: SystemMetricsSnapshot;
  pending: boolean;
  error?: string;
  onRetry: () => void;
};

type ChartPoint = {
  sampledAt: string;
  time: string;
  cpu?: number;
  memory?: number;
  receive?: number;
  send?: number;
  read?: number;
  write?: number;
};

const percentDomain: [number, number] = [0, 100];

// 磁盘容量条与图标按顺序从色板取色，同一磁盘固定同一颜色，图例见磁盘容量区说明。
const DISK_COLORS = ['#7c3aed', '#0ea5e9', '#f59e0b', '#10b981', '#f43f5e', '#0284c7'];

export function SystemResourceCharts({ metrics, pending, error, onRetry }: SystemResourceChartsProps) {
  if (pending && !metrics) {
    return (
      <section className="mt-6" aria-label="系统资源">
        <SectionHeading />
        <div className="grid gap-4 sm:grid-cols-2">
          {['CPU 使用率', '内存使用率', '网络速度'].map((title) => (
            <Card key={title} className="min-w-0">
              <CardHeader><CardTitle>{title}</CardTitle></CardHeader>
              <CardContent><div className="skeleton h-44" /></CardContent>
            </Card>
          ))}
          <Card className="min-w-0 sm:col-span-2">
            <CardHeader><CardTitle>磁盘</CardTitle></CardHeader>
            <CardContent><div className="skeleton h-44" /></CardContent>
          </Card>
        </div>
      </section>
    );
  }

  if (!metrics) {
    return (
      <section className="mt-6" aria-label="系统资源">
        <SectionHeading />
        <div className="flex min-h-28 items-center justify-between gap-4 rounded-xl border border-red-200 bg-red-50 px-5 py-4" role="alert">
          <p className="text-sm text-red-700">{error ?? '无法读取系统资源'}</p>
          <Button type="button" variant="outline" onClick={onRetry}>
            <RefreshCw />
            重试
          </Button>
        </div>
      </section>
    );
  }

  const points = metrics.samples.map(chartPoint);
  const latest = points.at(-1);
  const memoryDetail = metrics.memory ? `${formatBytes(metrics.memory.usedBytes)} / ${formatBytes(metrics.memory.totalBytes)}` : '当前不可用';

  return (
    <section className="mt-6" aria-label="系统资源">
      <SectionHeading sampledAt={metrics.sampledAt} historyWindowSeconds={metrics.historyWindowSeconds} />
      {error ? <p className="mb-3 text-xs text-amber-700" role="status">实时刷新暂时失败，保留最近一次采样。</p> : null}
      <div className="grid min-w-0 gap-4 sm:grid-cols-2">
        <MetricCard
          title="CPU 使用率"
          ariaLabel="CPU 使用率图表"
          icon={Cpu}
          value={metrics.availability.cpu ? formatPercent(latest?.cpu) : '不可用'}
          detail="API 主机"
        >
          <PercentChart points={points} dataKey="cpu" name="CPU" color="#059669" />
        </MetricCard>

        <MetricCard
          title="内存使用率"
          ariaLabel="内存使用率图表"
          icon={MemoryStick}
          value={metrics.availability.memory ? formatPercent(latest?.memory) : '不可用'}
          detail={memoryDetail}
        >
          <PercentChart points={points} dataKey="memory" name="内存" color="#2563eb" />
        </MetricCard>

        <MetricCard
          title="网络速度"
          ariaLabel="网络速度图表"
          icon={Network}
          value={metrics.availability.network ? `接收 ${formatRate(latest?.receive)}` : '不可用'}
          detail={metrics.availability.network ? `发送 ${formatRate(latest?.send)}` : '主机网络计数器不可用'}
        >
          <RateChart
            points={points}
            lines={[
              { key: 'receive', name: '接收', color: '#0284c7' },
              { key: 'send', name: '发送', color: '#d97706' },
            ]}
          />
        </MetricCard>

        <Card
          className="min-w-0 sm:col-span-2"
          aria-label="磁盘"
        >
          <CardHeader>
            <div className="flex min-w-0 items-start justify-between gap-4">
              <div className="min-w-0">
                <CardTitle>磁盘</CardTitle>
                <p className="mt-1 text-xs text-zinc-500">容量与 I/O 实时负载</p>
              </div>
              <HardDrive aria-hidden="true" className="mt-0.5 size-5 shrink-0 text-zinc-400" />
            </div>
          </CardHeader>
          <CardContent>
            <div className="flex min-w-0 flex-wrap items-baseline justify-between gap-x-4 gap-y-1" aria-label="磁盘资源图表">
              <p className="text-2xl font-semibold tabular-nums text-zinc-950">
                {metrics.availability.diskIO ? `读取 ${formatRate(latest?.read)}` : 'I/O 不可用'}
              </p>
              <p className="text-xs text-zinc-500">
                {metrics.availability.diskIO ? `写入 ${formatRate(latest?.write)}` : '主机磁盘计数器不可用'}
              </p>
            </div>
            <div className="mt-3">
              <RateChart
                points={points}
                lines={[
                  { key: 'read', name: '读取', color: '#7c3aed' },
                  { key: 'write', name: '写入', color: '#dc2626' },
                ]}
              />
            </div>
            <div className="mt-4 border-t border-zinc-100 pt-4" aria-label="磁盘容量">
              <div className="flex items-baseline justify-between gap-3">
                <p className="text-sm font-medium text-zinc-700">磁盘容量</p>
                <p className="shrink-0 text-xs text-zinc-500">不同颜色代表不同磁盘设备</p>
              </div>
              {metrics.disks.length === 0 ? (
                <p className="mt-2 text-xs text-zinc-500">磁盘容量不可用</p>
              ) : (
                <ul className="mt-3 grid max-h-56 gap-x-6 gap-y-3 overflow-y-auto overscroll-contain pr-1 sm:grid-cols-2">
                  {metrics.disks.map((disk, index) => (
                    <DiskUsageRow key={disk.device} disk={disk} color={diskColor(index)} />
                  ))}
                </ul>
              )}
            </div>
          </CardContent>
        </Card>
      </div>
    </section>
  );
}

function diskColor(index: number): string {
  return DISK_COLORS[index % DISK_COLORS.length];
}

// deviceName 展示设备短名：Linux 块设备去掉 /dev/ 前缀（/dev/sda1 → sda1），
// Windows 盘符与 overlay 等伪设备保持原样。
function deviceName(device: string): string {
  return device.replace(/^\/dev\//, '');
}

function DiskUsageRow({ disk, color }: { disk: SystemDiskUsage; color: string }) {
  const name = deviceName(disk.device);
  const showPath = disk.path !== '' && disk.path !== disk.device;
  const fullTitle = showPath ? `${disk.device} · ${disk.path}` : disk.device;
  return (
    <li aria-label={`${name} 磁盘容量`}>
      {/* 窄屏（<640px）分行展示避免横向溢出：设备名一行，容量文本换到下一行。 */}
      <div className="flex flex-col gap-1 text-xs sm:flex-row sm:items-center sm:justify-between sm:gap-3">
        <span className="flex min-w-0 items-center gap-2">
          <span className="size-2.5 shrink-0 rounded-full" style={{ backgroundColor: color }} aria-hidden="true" />
          <span className="truncate font-medium text-zinc-700" title={fullTitle}>{name}</span>
          {showPath ? <span className="truncate text-zinc-400" title={fullTitle}>{disk.path}</span> : null}
        </span>
        <span className="shrink-0 pl-4 text-zinc-500 sm:pl-0">{formatBytes(disk.usedBytes)} / {formatBytes(disk.totalBytes)} · {formatPercent(disk.usedPercent)}</span>
      </div>
      <div className="mt-1.5 h-2 overflow-hidden rounded-sm bg-zinc-100">
        <div className="h-full" style={{ width: `${Math.min(100, Math.max(0, disk.usedPercent))}%`, backgroundColor: color }} />
      </div>
    </li>
  );
}

function SectionHeading({ sampledAt, historyWindowSeconds }: { sampledAt?: string; historyWindowSeconds?: number }) {
  return (
    <div className="mb-3 flex min-w-0 items-end justify-between gap-3">
      <div>
        <h2 className="text-base font-semibold text-zinc-950">系统资源</h2>
        <p className="mt-1 text-xs text-zinc-500">API 主机实时负载</p>
      </div>
      {sampledAt ? (
        <p className="shrink-0 text-right text-xs text-zinc-500">
          最近 {Math.round((historyWindowSeconds ?? 120) / 60)} 分钟 · {formatDateTime(sampledAt)}
        </p>
      ) : null}
    </div>
  );
}

function MetricCard({ title, ariaLabel, icon: Icon, value, detail, children }: {
  title: string;
  ariaLabel: string;
  icon: typeof Cpu;
  value: string;
  detail: string;
  children: React.ReactNode;
}) {
  return (
    <Card className="min-w-0" aria-label={ariaLabel}>
      <CardHeader>
        <div className="flex min-w-0 items-start justify-between gap-4">
          <div className="min-w-0">
            <CardTitle>{title}</CardTitle>
            <p className="mt-2 truncate text-2xl font-semibold text-zinc-950">{value}</p>
            <p className="mt-1 truncate text-xs text-zinc-500">{detail}</p>
          </div>
          <Icon aria-hidden="true" className="mt-0.5 size-5 shrink-0 text-zinc-400" />
        </div>
      </CardHeader>
      <CardContent>{children}</CardContent>
    </Card>
  );
}

function PercentChart({ points, dataKey, name, color }: {
  points: ChartPoint[];
  dataKey: 'cpu' | 'memory';
  name: string;
  color: string;
}) {
  return (
    <ChartFrame>
      <LineChart data={points} margin={{ top: 8, right: 8, bottom: 0, left: -20 }}>
        <CartesianGrid stroke="#e4e4e7" strokeDasharray="3 3" vertical={false} />
        <XAxis dataKey="time" tick={{ fill: '#71717a', fontSize: 11 }} tickLine={false} axisLine={false} minTickGap={28} />
        <YAxis domain={percentDomain} ticks={[0, 50, 100]} tickFormatter={(value) => `${value}%`} tick={{ fill: '#71717a', fontSize: 11 }} tickLine={false} axisLine={false} />
        <Tooltip formatter={(value) => formatPercent(numberValue(value))} labelFormatter={(_, payload) => payload[0]?.payload?.sampledAt ? formatDateTime(payload[0].payload.sampledAt) : ''} />
        <Line type="monotone" dataKey={dataKey} name={name} stroke={color} strokeWidth={2} dot={false} connectNulls={false} isAnimationActive={false} />
      </LineChart>
    </ChartFrame>
  );
}

function RateChart({ points, lines }: {
  points: ChartPoint[];
  lines: Array<{ key: 'receive' | 'send' | 'read' | 'write'; name: string; color: string }>;
}) {
  return (
    <ChartFrame>
      <LineChart data={points} margin={{ top: 8, right: 8, bottom: 0, left: -8 }}>
        <CartesianGrid stroke="#e4e4e7" strokeDasharray="3 3" vertical={false} />
        <XAxis dataKey="time" tick={{ fill: '#71717a', fontSize: 11 }} tickLine={false} axisLine={false} minTickGap={28} />
        <YAxis width={52} tickFormatter={(value) => compactRate(Number(value))} tick={{ fill: '#71717a', fontSize: 11 }} tickLine={false} axisLine={false} />
        <Tooltip formatter={(value) => formatRate(numberValue(value))} labelFormatter={(_, payload) => payload[0]?.payload?.sampledAt ? formatDateTime(payload[0].payload.sampledAt) : ''} />
        <Legend iconType="plainline" iconSize={12} wrapperStyle={{ color: '#52525b', fontSize: 11 }} />
        {lines.map((line) => (
          <Line key={line.key} type="monotone" dataKey={line.key} name={line.name} stroke={line.color} strokeWidth={2} dot={false} connectNulls={false} isAnimationActive={false} />
        ))}
      </LineChart>
    </ChartFrame>
  );
}

function ChartFrame({ children }: { children: React.ReactElement }) {
  return (
    <div className="h-44 min-w-0 w-full" role="img" aria-label="最近两分钟趋势">
      <ResponsiveContainer width="100%" height="100%" minWidth={0}>
        {children}
      </ResponsiveContainer>
    </div>
  );
}

function chartPoint(sample: SystemMetricSample): ChartPoint {
  const sampledAt = new Date(sample.sampledAt);
  return {
    sampledAt: sample.sampledAt,
    time: Number.isNaN(sampledAt.getTime()) ? '' : sampledAt.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' }),
    cpu: sample.cpuUsedPercent,
    memory: sample.memoryUsedPercent,
    receive: sample.networkReceiveBytesPerSecond,
    send: sample.networkSendBytesPerSecond,
    read: sample.diskReadBytesPerSecond,
    write: sample.diskWriteBytesPerSecond,
  };
}

function formatPercent(value: number | undefined): string {
  if (value === undefined || !Number.isFinite(value)) return '—';
  return `${new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 1 }).format(value)}%`;
}

function formatRate(value: number | undefined): string {
  return value === undefined || !Number.isFinite(value) ? '—' : `${formatBytes(value)}/s`;
}

function compactRate(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0';
  if (value >= 1024 ** 3) return `${(value / 1024 ** 3).toFixed(1)}G`;
  if (value >= 1024 ** 2) return `${(value / 1024 ** 2).toFixed(1)}M`;
  if (value >= 1024) return `${(value / 1024).toFixed(0)}K`;
  return String(Math.round(value));
}

function numberValue(value: unknown): number | undefined {
  return typeof value === 'number' ? value : undefined;
}

export default SystemResourceCharts;
