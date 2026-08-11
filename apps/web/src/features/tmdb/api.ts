import { unwrap } from '@/api/app-client';
import { getTmDbSeriesCatalog, searchTmDbMovies, searchTmDbSeries, syncTmDbSeries } from '@/api/generated/sdk.gen';

// Re-exported SDK names use camelCase TmDb per the generator.
import type { CatalogCommandAccepted, TmDbMovieSearchResultPage, TmDbSeriesCatalog, TmDbSeriesSearchResultPage } from '@/api/generated/types.gen';

export function searchSeries(query: string): Promise<TmDbSeriesSearchResultPage> {
  return unwrap<TmDbSeriesSearchResultPage>(searchTmDbSeries({ query: { query } }), 'TMDb 查询失败');
}

export function searchMovies(query: string): Promise<TmDbMovieSearchResultPage> {
  return unwrap<TmDbMovieSearchResultPage>(searchTmDbMovies({ query: { query } }), 'TMDb 电影查询失败');
}

export function fetchSeriesCatalog(tmdbSeriesId: number): Promise<TmDbSeriesCatalog> {
  return unwrap<TmDbSeriesCatalog>(getTmDbSeriesCatalog({ path: { tmdbSeriesId } }), '无法读取 TMDb catalog');
}

export function syncSeries(key: string, tmdbSeriesId: number, seriesTitle: string): Promise<CatalogCommandAccepted> {
  return unwrap<CatalogCommandAccepted>(
    syncTmDbSeries({ path: { tmdbSeriesId }, headers: { 'Idempotency-Key': key }, body: { seriesTitle } }),
    '同步失败',
  );
}
