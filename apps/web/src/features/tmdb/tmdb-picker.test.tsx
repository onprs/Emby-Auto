import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it, vi } from 'vitest';

import { TMDbMoviePicker } from '@/features/tmdb/movie-picker';
import { TMDbSeriesPicker } from '@/features/tmdb/series-picker';
import { server } from '@/test/msw-server';
import { renderWithProviders } from '@/test/render';

describe('TMDb 作品查询', () => {
  it('同一关键词查询失败后再次查询会发出新请求', async () => {
    const queries: string[] = [];
    let attempts = 0;
    server.use(
      http.get('*/api/v1/tmdb/series/search', ({ request }) => {
        queries.push(new URL(request.url).searchParams.get('query') ?? '');
        attempts += 1;
        if (attempts === 1) {
          return HttpResponse.json(
            { code: 'tmdb_unavailable', message: 'TMDb 暂时不可用', details: {}, requestId: 'tmdb-test' },
            { status: 503 },
          );
        }
        return HttpResponse.json({ items: [{ tmdbSeriesId: 42, name: '重试成功作品' }] });
      }),
    );
    const onChange = vi.fn();
    renderWithProviders(<TMDbSeriesPicker value={null} onChange={onChange} />);

    await userEvent.type(await screen.findByLabelText('TMDb 作品'), '重试作品');
    await userEvent.click(screen.getByRole('button', { name: '查询' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('TMDb 暂时不可用');

    // 关键词不变时直接点“查询”也必须重试，而不是保持错误状态不变。
    await userEvent.click(screen.getByRole('button', { name: '查询' }));
    const candidate = await screen.findByRole('button', { name: /重试成功作品/ });
    expect(queries).toEqual(['重试作品', '重试作品']);

    await userEvent.click(candidate);
    expect(onChange).toHaveBeenCalledWith({ tmdbSeriesId: 42, title: '重试成功作品' });
  });

  it('电影查询同样支持同关键词失败后重试', async () => {
    const queries: string[] = [];
    let attempts = 0;
    server.use(
      http.get('*/api/v1/tmdb/movies/search', ({ request }) => {
        queries.push(new URL(request.url).searchParams.get('query') ?? '');
        attempts += 1;
        if (attempts === 1) {
          return HttpResponse.json(
            { code: 'tmdb_unavailable', message: 'TMDb 电影服务不可用', details: {}, requestId: 'tmdb-movie-test' },
            { status: 503 },
          );
        }
        return HttpResponse.json({ items: [{ tmdbMovieId: 7, title: '重试成功电影', releaseYear: 2026 }] });
      }),
    );
    renderWithProviders(<TMDbMoviePicker value={null} onChange={() => undefined} />);

    await userEvent.type(await screen.findByLabelText('TMDb 电影'), '重试电影');
    await userEvent.click(screen.getByRole('button', { name: '查询' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('TMDb 电影服务不可用');

    await userEvent.click(screen.getByRole('button', { name: '查询' }));
    expect(await screen.findByRole('button', { name: /重试成功电影/ })).toBeInTheDocument();
    expect(queries).toEqual(['重试电影', '重试电影']);
  });
});
