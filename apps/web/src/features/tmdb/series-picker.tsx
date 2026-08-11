import { useQuery } from '@tanstack/react-query';
import { useEffect, useState } from 'react';

import { searchSeries } from '@/features/tmdb/api';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { formatDate } from '@/lib/format';

export type SeriesSelection = {
  tmdbSeriesId: number;
  title: string;
};

/**
 * Lets the administrator find a TMDb TV series by name without knowing its ID.
 */
export function TMDbSeriesPicker({ value, onChange }: { value: SeriesSelection | null; onChange: (selection: SeriesSelection | null) => void }) {
  const [query, setQuery] = useState('');
  const [submitted, setSubmitted] = useState('');

  const results = useQuery({
    queryKey: ['tmdb-search', submitted],
    queryFn: () => searchSeries(submitted),
    enabled: submitted.length > 0,
    retry: false,
  });

  useEffect(() => {
    if (value) {
      setSubmitted('');
    }
  }, [value]);

  if (value) {
    return (
      <div className="space-y-2">
        <Label>TMDb 作品</Label>
        <div className="flex items-center justify-between gap-3 border border-emerald-200 bg-emerald-50 px-3 py-2">
          <span className="text-sm text-emerald-900">
            {value.title} <span className="text-emerald-600">(ID {value.tmdbSeriesId})</span>
          </span>
          <Button type="button" variant="ghost" onClick={() => onChange(null)}>
            更换
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-2">
      <Label htmlFor="tmdb-query">TMDb 作品</Label>
      <div className="flex gap-2">
        <Input
          id="tmdb-query"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="输入作品名称查询"
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              event.preventDefault();
              setSubmitted(query.trim());
            }
          }}
        />
        <Button type="button" variant="outline" onClick={() => setSubmitted(query.trim())} disabled={!query.trim() || results.isFetching}>
          查询
        </Button>
      </div>
      {submitted ? (
        results.isPending ? (
          <p className="text-sm text-zinc-500">查询中…</p>
        ) : results.error ? (
          <p className="text-sm text-red-600" role="alert">
            {results.error.message}
          </p>
        ) : results.data && results.data.items.length === 0 ? (
          <p className="text-sm text-zinc-500">未找到匹配作品</p>
        ) : (
          <ul className="max-h-64 divide-y divide-zinc-100 overflow-auto border border-zinc-200 bg-white">
            {results.data?.items.map((item) => (
              <li key={item.tmdbSeriesId}>
                <button
                  type="button"
                  className="w-full px-3 py-2 text-left hover:bg-zinc-50"
                  onClick={() => onChange({ tmdbSeriesId: item.tmdbSeriesId, title: item.name })}
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
        )
      ) : null}
    </div>
  );
}
