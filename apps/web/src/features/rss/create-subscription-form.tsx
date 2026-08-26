import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { LoaderCircle, SearchCheck } from 'lucide-react';
import { useRef, useState } from 'react';

import { fetchAgentResolution } from '@/features/agent/api';
import { createSubscription, lookupFeed } from '@/features/rss/api';
import { parseKeywordInput } from '@/features/rss/keyword-input';
import { searchSeries } from '@/features/tmdb/api';
import type { AgentCatalogCandidateProposal, TmDbSeriesSearchResult } from '@/api/generated/types.gen';
import type { SeriesSelection } from '@/features/tmdb/series-picker';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { ErrorState } from '@/components/ui/feedback';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { formatDate } from '@/lib/format';

type MatchMode = 'idle' | 'auto' | 'agent' | 'manual';

/**
 * Creates an RSS subscription. The TMDb series is matched automatically from
 * the feed URL; manual keyword correction is the fallback.
 */
export function CreateSubscriptionForm({ onDone }: { onDone: () => void }) {
  const queryClient = useQueryClient();
  const [feedUrl, setFeedUrl] = useState('');
  const [subscriptionName, setSubscriptionName] = useState('');
  const [series, setSeries] = useState<SeriesSelection | null>(null);
  const [mode, setMode] = useState<MatchMode>('idle');
  const [keyword, setKeyword] = useState('');
  const [submitted, setSubmitted] = useState('');
  const [automaticCandidates, setAutomaticCandidates] = useState<TmDbSeriesSearchResult[]>([]);
  const [agentResolutionId, setAgentResolutionId] = useState('');
  const [sourceSeason, setSourceSeason] = useState('1');
  const [pollIntervalMinutes, setPollIntervalMinutes] = useState('15');
  const [includeKeywords, setIncludeKeywords] = useState('');
  const [excludeKeywords, setExcludeKeywords] = useState('');
  const [cleanupSourceOnCompletion, setCleanupSourceOnCompletion] = useState(false);
  const [autoEpisodeMapping, setAutoEpisodeMapping] = useState(false);
  const [autoReview, setAutoReview] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const lastLookupUrl = useRef('');

  const lookup = useMutation({
    mutationFn: lookupFeed,
    onSuccess: (result) => {
      const suggestion = result.suggestedQuery || result.feedTitle || '';
      setKeyword(suggestion);
      setAutomaticCandidates(result.candidates);
      setAgentResolutionId(result.agentResolutionId ?? '');
      setSubmitted('');
      if (result.feedTitle && !subscriptionName) {
        const groupMatch = result.feedTitle.match(/^\[([^\]]+)\]/);
        setSubscriptionName(groupMatch ? groupMatch[1] : result.feedTitle);
      }
      if (result.candidates.length > 0) {
        setMode('auto');
      } else if (result.catalogMatchSource === 'agent_pending' && result.agentResolutionId) {
        setMode('agent');
      } else {
        setMode('manual');
      }
    },
    onError: () => {
      lastLookupUrl.current = '';
      setMode('manual');
    },
  });

  const results = useQuery({
    queryKey: ['tmdb-search', submitted],
    queryFn: () => searchSeries(submitted),
    enabled: submitted.length > 0,
    retry: false,
  });

  const agentResolution = useQuery({
    queryKey: ['agent-resolution', agentResolutionId],
    queryFn: () => fetchAgentResolution(agentResolutionId),
    enabled: agentResolutionId.length > 0,
    retry: false,
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      return !status || status === 'queued' || status === 'running' ? 1_000 : false;
    },
  });
  const agentProposal = agentResolution.data?.status === 'review_required'
    && agentResolution.data.validation?.verdict === 'review_required'
    && agentResolution.data.proposal?.capability === 'catalog_candidate'
    ? agentResolution.data.proposal as AgentCatalogCandidateProposal
    : undefined;
  const agentCandidateSearch = useQuery({
    queryKey: ['tmdb-search', 'agent', agentResolutionId, agentProposal?.query],
    queryFn: () => searchSeries(agentProposal!.query),
    enabled: Boolean(agentProposal?.query),
    retry: false,
  });
  const agentCandidateIds = new Set(agentProposal?.candidateIds ?? []);
  const agentCandidates = agentCandidateSearch.data?.items.filter((item) => agentCandidateIds.has(item.tmdbSeriesId)) ?? [];
  const visibleCandidates = mode === 'manual' ? (results.data?.items ?? []) : mode === 'agent' ? agentCandidates : automaticCandidates;
  const agentBusy = mode === 'agent' && (
    agentResolution.isPending
    || agentResolution.data?.status === 'queued'
    || agentResolution.data?.status === 'running'
    || (Boolean(agentProposal) && agentCandidateSearch.isPending)
  );

  const create = useMutation({
    mutationFn: () => {
      if (!series) {
        throw new Error('请确认 TMDb 作品');
      }
      return createSubscription({
        tmdbSeriesId: series.tmdbSeriesId,
        seriesTitle: series.title,
        name: subscriptionName.trim() || series.title,
        feedUrl: feedUrl.trim(),
        includeKeywords: parseKeywordInput(includeKeywords),
        excludeKeywords: parseKeywordInput(excludeKeywords),
        enabled: true,
        autoEpisodeMapping,
        autoReview,
        cleanupSourceOnCompletion,
        sourceSeason: Number(sourceSeason),
        pollIntervalSeconds: Number(pollIntervalMinutes) * 60,
      });
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['rss'] });
      onDone();
    },
    onError: (cause) => setError(cause instanceof Error ? cause.message : '创建订阅失败'),
  });

  const identify = () => {
    const trimmed = feedUrl.trim();
    if (!/^https?:\/\//.test(trimmed) || trimmed === lastLookupUrl.current) {
      return;
    }
    lastLookupUrl.current = trimmed;
    setSeries(null);
    setSubmitted('');
    setAutomaticCandidates([]);
    setAgentResolutionId('');
    setMode('idle');
    lookup.mutate(trimmed);
  };

  const selectSeries = (selection: SeriesSelection) => {
    setSeries(selection);
    setSubmitted('');
    setMode('idle');
  };

  const showCandidates = mode !== 'idle' && !series && visibleCandidates.length > 0;
  const showNoCandidates = !series && (
    (mode === 'manual' && (
      (submitted.length > 0 && results.data !== undefined && visibleCandidates.length === 0)
      || (submitted.length === 0 && lookup.data?.catalogMatchSource === 'none')
    ))
    || (mode === 'agent' && !agentBusy && visibleCandidates.length === 0)
  );
  const showManual = !series && (
    (mode !== 'idle' && mode !== 'agent')
    || (mode === 'agent' && !agentBusy)
  );

  return (
    <Card className="mb-6">
      <CardHeader>
        <CardTitle>新建订阅</CardTitle>
        <CardDescription>填写 RSS 地址后自动识别并匹配 TMDb 作品</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="space-y-2">
          <Label htmlFor="rss-url">RSS 地址</Label>
          <div className="flex gap-2">
            <Input
              id="rss-url"
              type="url"
              value={feedUrl}
              onChange={(event) => {
                setFeedUrl(event.target.value);
                setSeries(null);
                setMode('idle');
                setSubmitted('');
                setAutomaticCandidates([]);
                setAgentResolutionId('');
              }}
              onBlur={identify}
              placeholder="https://…"
              required
            />
            <Button type="button" variant="outline" onClick={identify} disabled={lookup.isPending || !/^https?:\/\//.test(feedUrl.trim())}>
              <SearchCheck />
              识别作品
            </Button>
          </div>
        </div>

        {series ? (
          <div className="space-y-2">
            <Label>TMDb 作品</Label>
            <div className="flex items-center justify-between gap-3 rounded-lg border border-emerald-200 bg-emerald-50 px-3 py-2">
              <span className="text-sm text-emerald-900">
                {series.title} <span className="text-emerald-600">(ID {series.tmdbSeriesId})</span>
              </span>
              <Button type="button" variant="ghost" onClick={() => { setSeries(null); setMode('manual'); }}>
                更换
              </Button>
            </div>
          </div>
        ) : (
          <div className="space-y-2">
            {lookup.isPending ? (
              <p className="flex items-center gap-2 text-sm text-zinc-500" role="status">
                <LoaderCircle className="size-4 animate-spin" />
                正在识别 RSS 内容…
              </p>
            ) : null}
            {agentBusy ? (
              <p className="flex items-center gap-2 text-sm text-zinc-500" role="status">
                <LoaderCircle className="size-4 animate-spin" />
                Agent 正在匹配 TMDb 作品…
              </p>
            ) : null}
            {lookup.isError ? (
              <p className="text-sm text-amber-700" role="alert">无法自动识别 RSS 内容，请手动搜索作品。</p>
            ) : null}
            {mode === 'manual' && results.error ? (
              <p className="text-sm text-red-600" role="alert">{results.error.message}</p>
            ) : null}
            {mode === 'agent' && !agentBusy && (agentResolution.isError || agentCandidateSearch.isError || !agentProposal) ? (
              <p className="text-sm text-amber-700" role="alert">Agent 未能确认 TMDb 作品，请手动搜索。</p>
            ) : null}
            {showNoCandidates ? (
              <p className="text-sm text-zinc-500">未找到匹配作品，请修正关键词后重试。</p>
            ) : null}
            {showCandidates ? (
              <>
                <Label>{mode === 'manual' ? '选择匹配的作品' : '自动匹配到以下作品，请选择'}</Label>
                <ul className="max-h-64 divide-y divide-zinc-100 overflow-auto border border-zinc-200 bg-white">
                  {visibleCandidates.map((item) => (
                    <li key={item.tmdbSeriesId}>
                      <button
                        type="button"
                        className="w-full px-3 py-2 text-left hover:bg-zinc-50"
                        onClick={() => selectSeries({ tmdbSeriesId: item.tmdbSeriesId, title: item.name })}
                      >
                        <span className="block text-sm font-medium text-zinc-900">{item.name}</span>
                        <span className="block text-xs text-zinc-500">
                          {item.originalName && item.originalName !== item.name ? `${item.originalName} · ` : ''}
                          {item.firstAirDate ? `${formatDate(item.firstAirDate)} · ` : ''}ID {item.tmdbSeriesId}
                        </span>
                      </button>
                    </li>
                  ))}
                </ul>
              </>
            ) : null}
            {showManual ? (
              <div className="space-y-2">
                <Label htmlFor="rss-tmdb-keyword">作品搜索关键词</Label>
                <div className="flex gap-2">
                  <Input
                    id="rss-tmdb-keyword"
                    value={keyword}
                    onChange={(event) => setKeyword(event.target.value)}
                    placeholder="输入作品名称查询"
                    onKeyDown={(event) => {
                      if (event.key === 'Enter') {
                        event.preventDefault();
                        setMode('manual');
                        setSubmitted(keyword.trim());
                      }
                    }}
                  />
                  <Button
                    type="button"
                    variant="outline"
                    onClick={() => { setMode('manual'); setSubmitted(keyword.trim()); }}
                    disabled={!keyword.trim() || results.isFetching}
                  >
                    查询
                  </Button>
                </div>
              </div>
            ) : null}
          </div>
        )}

        <div className="space-y-2">
          <Label htmlFor="rss-subscription-name">订阅源名称（如字幕组名）</Label>
          <Input
            id="rss-subscription-name"
            value={subscriptionName}
            onChange={(event) => setSubscriptionName(event.target.value)}
            placeholder="例如：字幕组名称或发布版本（留空默认使用番剧名）"
          />
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-2">
            <Label htmlFor="rss-include-keywords">包含词</Label>
            <Input
              id="rss-include-keywords"
              value={includeKeywords}
              onChange={(event) => setIncludeKeywords(event.target.value)}
              placeholder="例如：简日, 1080p"
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="rss-exclude-keywords">不包含词</Label>
            <Input
              id="rss-exclude-keywords"
              value={excludeKeywords}
              onChange={(event) => setExcludeKeywords(event.target.value)}
              placeholder="例如：720p, 合集"
            />
          </div>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-2">
            <Label htmlFor="rss-season">资源对应第几季</Label>
            <Input id="rss-season" type="number" min={1} value={sourceSeason} onChange={(event) => setSourceSeason(event.target.value)} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="rss-interval">每隔多少分钟检查</Label>
            <Input id="rss-interval" type="number" min={1} max={1440} value={pollIntervalMinutes} onChange={(event) => setPollIntervalMinutes(event.target.value)} />
          </div>
        </div>
        <label className="flex items-start gap-2.5 border border-zinc-200 bg-zinc-50 px-3 py-2.5 text-sm text-zinc-800">
          <input
            type="checkbox"
            className="mt-0.5 size-4 shrink-0 accent-emerald-700"
            checked={autoEpisodeMapping}
            onChange={(event) => setAutoEpisodeMapping(event.target.checked)}
          />
          <span>下载文件确认后自动完成剧集映射，无法唯一判断时使用已启用的 Agent</span>
        </label>
        <label className="flex items-start gap-2.5 border border-zinc-200 bg-zinc-50 px-3 py-2.5 text-sm text-zinc-800">
          <input
            type="checkbox"
            className="mt-0.5 size-4 shrink-0 accent-emerald-700"
            checked={autoReview}
            onChange={(event) => setAutoReview(event.target.checked)}
          />
          <span>媒体处理完成后自动审核并入库到 Emby</span>
        </label>
        <label className="flex items-start gap-2.5 border border-zinc-200 bg-zinc-50 px-3 py-2.5 text-sm text-zinc-800">
          <input
            type="checkbox"
            className="mt-0.5 size-4 shrink-0 accent-emerald-700"
            checked={cleanupSourceOnCompletion}
            onChange={(event) => setCleanupSourceOnCompletion(event.target.checked)}
          />
          <span>最终集入库后，删除对应的 qBittorrent 种子和缓存文件</span>
        </label>
        {error ? <ErrorState message={error} /> : null}
        <div className="flex gap-2">
          <Button type="button" onClick={() => create.mutate()} disabled={create.isPending || !series || !feedUrl.trim()}>
            创建订阅
          </Button>
          <Button type="button" variant="outline" onClick={onDone}>
            取消
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
