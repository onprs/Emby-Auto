import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';

import { SettingsSubtitleGroupsPage } from '@/features/configuration/settings-subtitle-groups-page';
import { server } from '@/test/msw-server';
import { renderWithProviders } from '@/test/render';

const group = {
  id: '10000000-0000-0000-0000-000000000001',
  name: 'LoliHouse',
  createdAt: '2026-01-01T00:00:00Z',
  updatedAt: '2026-01-01T00:00:00Z',
};

describe('SettingsSubtitleGroupsPage', () => {
  it('adds a subtitle group to the maintained list', async () => {
    let created: unknown;
    server.use(
      http.get('*/api/v1/rss/subtitle-groups', () => HttpResponse.json({ items: [] })),
      http.post('*/api/v1/rss/subtitle-groups', async ({ request }) => {
        created = await request.json();
        return HttpResponse.json(group, { status: 201 });
      }),
    );
    renderWithProviders(<SettingsSubtitleGroupsPage />);

    await userEvent.type(await screen.findByLabelText('字幕组名称'), 'LoliHouse');
    await userEvent.click(screen.getByRole('button', { name: '添加字幕组' }));

    await waitFor(() => expect(created).toEqual({ name: 'LoliHouse' }));
  });

  it('allows removing a maintained subtitle group', async () => {
    let deleted = '';
    server.use(
      http.get('*/api/v1/rss/subtitle-groups', () => HttpResponse.json({ items: [group] })),
      http.delete('*/api/v1/rss/subtitle-groups/:groupId', ({ params }) => {
        deleted = String(params.groupId);
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderWithProviders(<SettingsSubtitleGroupsPage />);

    await userEvent.click(await screen.findByRole('button', { name: '删除 LoliHouse' }));
    await waitFor(() => expect(deleted).toBe(group.id));
  });
});
