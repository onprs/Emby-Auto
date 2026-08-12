import { Link } from '@tanstack/react-router';
import { ArrowRight, Bot, Download, FileCog, Film, FolderCog, Globe, Plug, Rss } from 'lucide-react';

import { PageBody, PageHeader, DetailErrorState, DetailLoadingState } from '@/components/resource';
import { Badge } from '@/components/ui/badge';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { useSettingsData } from '@/features/configuration/use-settings';
import { cn } from '@/lib/utils';

export function SettingsHubPage() {
  const { configuration, configurationPending, configurationError, refetchConfiguration, dependencies } = useSettingsData();

  if (configurationPending) {
    return <DetailLoadingState title="设置" label="正在读取配置" />;
  }
  if (configurationError || !configuration) {
    return <DetailErrorState title="设置" message={configurationError?.message ?? '无法读取配置'} onRetry={() => refetchConfiguration()} />;
  }

  return (
    <PageBody>
      <PageHeader title="设置" description={`配置版本 ${configuration.version} · 各服务状态总览`} />

      <div className="grid gap-5 sm:grid-cols-2 xl:grid-cols-3">
        <HubCard
          to="/settings/services"
          icon={Download}
          title="qBittorrent"
          status={configuration.qBittorrent.password.configured ? { tone: 'success', label: '已配置' } : { tone: 'warning', label: '密码未配置' }}
          description={configuration.qBittorrent.url || '未设置 URL'}
        />
        <HubCard
          to="/settings/services"
          icon={Film}
          title="Emby"
          status={configuration.emby.apiKey.configured ? { tone: 'success', label: '已配置' } : { tone: 'warning', label: 'API key 未配置' }}
          description={configuration.emby.url || '未设置 URL'}
        />
        <HubCard
          to="/settings/services"
          icon={Rss}
          title="TMDb"
          status={configuration.tmdb.apiToken.configured ? { tone: 'success', label: '已配置' } : { tone: 'warning', label: 'Token 未配置' }}
          description="元数据与剧集映射来源"
        />
        <HubCard
          to="/settings/services"
          icon={Globe}
          title="网络代理"
          status={configuration.networkProxy.enabled ? { tone: 'success', label: '已启用' } : { tone: 'neutral', label: '未启用' }}
          description={configuration.networkProxy.enabled ? configuration.networkProxy.url : '访问外部服务不使用代理'}
        />
        <HubCard
          to="/settings/agent"
          icon={Bot}
          title="Agent"
          status={!configuration.agent.enabled
            ? { tone: 'neutral', label: '已关闭' }
            : configuration.agent.apiKey.configured && configuration.agent.baseUrl && configuration.agent.model
              ? { tone: 'success', label: dependencies?.agent.lastTestSuccess ? '连接正常' : '待测试' }
              : { tone: 'warning', label: '配置不完整' }}
          description={configuration.agent.model || '异常资源辅助解析'}
        />
        <HubCard
          to="/settings/storage"
          icon={FolderCog}
          title="存储与媒体工具"
          status={{ tone: 'neutral', label: `${configuration.paths.ffmpegPath ? 'FFmpeg 已设置' : '未设置'}` }}
          description="下载、工作、暂存与媒体库目录"
        />
        <HubCard
          to="/settings/transcode"
          icon={FileCog}
          title="转码配置"
          status={{ tone: 'info', label: configuration.transcode.name }}
          description={`${configuration.transcode.videoCodec.toUpperCase()} · ${configuration.transcode.container} · 并发 ${configuration.transcode.maxConcurrency}`}
        />
      </div>

      {dependencies ? (
        <Card className="mt-6">
          <CardHeader><CardTitle>连接状态</CardTitle></CardHeader>
          <CardContent>
            <ul className="grid gap-x-6 sm:grid-cols-2 lg:grid-cols-3">
              <DependencyRow label="qBittorrent" status={dependencies.qBittorrent} />
              <DependencyRow label="Emby" status={dependencies.emby} />
              <DependencyRow label="TMDb" status={dependencies.tmdb} />
              <DependencyRow label="媒体工具" status={dependencies.mediaTools} />
              <DependencyRow label="网络代理" status={dependencies.networkProxy} />
              <DependencyRow label="Agent" status={dependencies.agent} />
            </ul>
          </CardContent>
        </Card>
      ) : null}

      <p className="mt-6 text-sm text-zinc-500">
        <Link to="/settings/services" className="inline-flex items-center gap-1 font-medium text-emerald-700 hover:underline">
          前往外部服务设置<ArrowRight className="size-4" aria-hidden="true" />
        </Link>
      </p>
    </PageBody>
  );
}

function HubCard({
  to,
  icon: Icon,
  title,
  status,
  description,
}: {
  to: string;
  icon: typeof Download;
  title: string;
  status: { tone: 'success' | 'warning' | 'neutral' | 'info'; label: string };
  description: string;
}) {
  return (
    <Link
      to={to}
      className="group flex items-start gap-4 rounded-xl border border-zinc-200/90 bg-white p-5 shadow-card transition-all duration-200 hover:-translate-y-0.5 hover:border-emerald-300 hover:shadow-card-hover"
    >
      <span className="grid size-11 shrink-0 place-items-center rounded-xl bg-zinc-50 text-zinc-600 ring-1 ring-zinc-200/80 transition-colors group-hover:bg-emerald-50 group-hover:text-emerald-700 group-hover:ring-emerald-200">
        <Icon className="size-5" aria-hidden="true" />
      </span>
      <span className="min-w-0 flex-1">
        <span className="flex items-center justify-between gap-2">
          <span className="text-sm font-semibold text-zinc-950">{title}</span>
          <Badge tone={status.tone}>{status.label}</Badge>
        </span>
        <span className="mt-1.5 block truncate text-sm text-zinc-500" title={description}>{description}</span>
      </span>
    </Link>
  );
}

function DependencyRow({ label, status }: { label: string; status: { configured: boolean; lastTestSuccess?: boolean; lastTestedAt?: string } }) {
  const healthy = status.configured && status.lastTestSuccess === true;
  return (
    <li className="flex items-center justify-between gap-3 border-b border-zinc-100 py-3 last:border-b-0">
      <span className="flex items-center gap-2 text-sm text-zinc-800"><Plug className="size-4 text-zinc-400" aria-hidden="true" />{label}</span>
      <span className={cn('flex items-center gap-1.5 text-sm font-medium', healthy ? 'text-emerald-700' : 'text-amber-700')}>
        <span className={cn('size-1.5 rounded-full', healthy ? 'bg-emerald-500' : 'bg-amber-500')} aria-hidden="true" />
        {healthy ? '可用' : status.configured ? '待检查' : '未设置'}
      </span>
    </li>
  );
}
