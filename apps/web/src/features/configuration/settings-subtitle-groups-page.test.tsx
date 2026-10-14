import { act, screen, waitFor } from '@testing-library/react';
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
const secondGroup = { ...group, id: '10000000-0000-0000-0000-000000000002', name: 'TranslationTeam' };

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => { resolve = done; });
  return { promise, resolve };
}

describe('SettingsSubtitleGroupsPage', () => {
  it('添加字幕组后刷新列表并清空已提交输入', async () => {
    let created: unknown;
    let items: typeof group[] = [];
    server.use(
      http.get('*/api/v1/rss/subtitle-groups', () => HttpResponse.json({ items })),
      http.post('*/api/v1/rss/subtitle-groups', async ({ request }) => {
        created = await request.json();
        items = [group];
        return HttpResponse.json(group, { status: 201 });
      }),
    );
    renderWithProviders(<SettingsSubtitleGroupsPage />);

    await userEvent.type(await screen.findByLabelText('字幕组名称'), 'LoliHouse');
    await userEvent.click(screen.getByRole('button', { name: '添加字幕组' }));

    await waitFor(() => expect(created).toEqual({ name: 'LoliHouse' }));
    expect(await screen.findByRole('button', { name: '编辑 LoliHouse' })).toBeVisible();
    expect(screen.getByLabelText('字幕组名称')).toHaveValue('');
  });

  it('删除返回无正文204后刷新列表并退出被删除记录的编辑', async () => {
    let items = [group];
    let deleted = '';
    server.use(
      http.get('*/api/v1/rss/subtitle-groups', () => HttpResponse.json({ items })),
      http.delete('*/api/v1/rss/subtitle-groups/:groupId', ({ params }) => {
        deleted = String(params.groupId);
        items = [];
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderWithProviders(<SettingsSubtitleGroupsPage />);

    await userEvent.click(await screen.findByRole('button', { name: '编辑 LoliHouse' }));
    await userEvent.click(screen.getByRole('button', { name: '删除 LoliHouse' }));

    expect(await screen.findByText('暂未维护字幕组')).toBeVisible();
    expect(deleted).toBe(group.id);
    expect(screen.queryByText('删除字幕组失败')).not.toBeInTheDocument();
    expect(screen.getByLabelText('字幕组名称')).toHaveValue('');
    expect(screen.getByRole('button', { name: '添加字幕组' })).toBeVisible();
  });

  it('正常重命名使用提交时的记录和名称并刷新列表', async () => {
    let items = [group];
    let saved: unknown;
    let savedId = '';
    server.use(
      http.get('*/api/v1/rss/subtitle-groups', () => HttpResponse.json({ items })),
      http.put('*/api/v1/rss/subtitle-groups/:groupId', async ({ params, request }) => {
        saved = await request.json();
        savedId = String(params.groupId);
        items = [{ ...group, name: 'ReleaseTranslationTeam' }];
        return HttpResponse.json(items[0]);
      }),
    );
    renderWithProviders(<SettingsSubtitleGroupsPage />);
    await userEvent.click(await screen.findByRole('button', { name: '编辑 LoliHouse' }));
    await userEvent.clear(screen.getByLabelText('字幕组名称'));
    await userEvent.type(screen.getByLabelText('字幕组名称'), ' ReleaseTranslationTeam ');
    await userEvent.click(screen.getByRole('button', { name: '保存名称' }));

    expect(await screen.findByRole('button', { name: '编辑 ReleaseTranslationTeam' })).toBeVisible();
    expect(savedId).toBe(group.id);
    expect(saved).toEqual({ name: 'ReleaseTranslationTeam' });
    expect(screen.getByLabelText('字幕组名称')).toHaveValue('');
  });

  it('旧保存请求完成后保留新选中记录的编辑草稿', async () => {
    const gate = deferred();
    let started = false;
    server.use(
      http.get('*/api/v1/rss/subtitle-groups', () => HttpResponse.json({ items: [group, secondGroup] })),
      http.put('*/api/v1/rss/subtitle-groups/:groupId', async () => {
        started = true;
        await gate.promise;
        return HttpResponse.json(group);
      }),
    );
    const { queryClient } = renderWithProviders(<SettingsSubtitleGroupsPage />);
    await userEvent.click(await screen.findByRole('button', { name: '编辑 LoliHouse' }));
    await userEvent.click(screen.getByRole('button', { name: '保存名称' }));
    await waitFor(() => expect(started).toBe(true));
    await userEvent.click(screen.getByRole('button', { name: '编辑 TranslationTeam' }));
    await userEvent.clear(screen.getByLabelText('字幕组名称'));
    await userEvent.type(screen.getByLabelText('字幕组名称'), 'TranslationCollective');
    await act(async () => gate.resolve());
    await waitFor(() => expect(queryClient.isMutating()).toBe(0));

    expect(screen.getByLabelText('字幕组名称')).toHaveValue('TranslationCollective');
    expect(screen.getByRole('button', { name: '保存名称' })).toBeVisible();
  });

  it('保存期间继续修改同一条记录时保留新输入', async () => {
    const gate = deferred();
    let started = false;
    server.use(
      http.get('*/api/v1/rss/subtitle-groups', () => HttpResponse.json({ items: [group] })),
      http.put('*/api/v1/rss/subtitle-groups/:groupId', async () => {
        started = true;
        await gate.promise;
        return HttpResponse.json(group);
      }),
    );
    const { queryClient } = renderWithProviders(<SettingsSubtitleGroupsPage />);
    await userEvent.click(await screen.findByRole('button', { name: '编辑 LoliHouse' }));
    await userEvent.click(screen.getByRole('button', { name: '保存名称' }));
    await waitFor(() => expect(started).toBe(true));
    await userEvent.clear(screen.getByLabelText('字幕组名称'));
    await userEvent.type(screen.getByLabelText('字幕组名称'), 'NewDraftTeam');
    await act(async () => gate.resolve());
    await waitFor(() => expect(queryClient.isMutating()).toBe(0));

    expect(screen.getByLabelText('字幕组名称')).toHaveValue('NewDraftTeam');
    expect(screen.getByRole('button', { name: '保存名称' })).toBeEnabled();
  });

  it('取消失败的编辑后清除该次编辑错误', async () => {
    server.use(
      http.get('*/api/v1/rss/subtitle-groups', () => HttpResponse.json({ items: [group] })),
      http.put('*/api/v1/rss/subtitle-groups/:groupId', () => HttpResponse.json({ code: 'rss_subtitle_group_exists', message: '字幕组已存在', details: {} }, { status: 409 })),
    );
    renderWithProviders(<SettingsSubtitleGroupsPage />);
    await userEvent.click(await screen.findByRole('button', { name: '编辑 LoliHouse' }));
    await userEvent.click(screen.getByRole('button', { name: '保存名称' }));
    await screen.findByText('字幕组已存在');
    await userEvent.click(screen.getByRole('button', { name: '取消编辑' }));

    expect(screen.queryByText('字幕组已存在')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: '添加字幕组' })).toBeVisible();
  });

  it('修改失败的名称后清除失效的错误提示', async () => {
    server.use(
      http.get('*/api/v1/rss/subtitle-groups', () => HttpResponse.json({ items: [group] })),
      http.put('*/api/v1/rss/subtitle-groups/:groupId', () => HttpResponse.json({ code: 'rss_subtitle_group_exists', message: '字幕组已存在', details: {} }, { status: 409 })),
    );
    renderWithProviders(<SettingsSubtitleGroupsPage />);
    await userEvent.click(await screen.findByRole('button', { name: '编辑 LoliHouse' }));
    await userEvent.click(screen.getByRole('button', { name: '保存名称' }));
    await screen.findByText('字幕组已存在');
    await userEvent.type(screen.getByLabelText('字幕组名称'), 'Updated');

    expect(screen.queryByText('字幕组已存在')).not.toBeInTheDocument();
  });

  it('真实删除错误仍提示失败并保留列表', async () => {
    server.use(
      http.get('*/api/v1/rss/subtitle-groups', () => HttpResponse.json({ items: [group] })),
      http.delete('*/api/v1/rss/subtitle-groups/:groupId', () => HttpResponse.json({ code: 'service_unavailable', message: '删除暂时不可用', details: {} }, { status: 503 })),
    );
    renderWithProviders(<SettingsSubtitleGroupsPage />);
    await userEvent.click(await screen.findByRole('button', { name: '删除 LoliHouse' }));

    expect(await screen.findByText('删除暂时不可用')).toBeVisible();
    expect(screen.getByRole('button', { name: '删除 LoliHouse' })).toBeEnabled();
  });
});
