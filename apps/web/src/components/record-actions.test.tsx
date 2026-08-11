import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import { RecordActions, type RecordAction } from '@/components/record-actions';

function baseActions(overrides: Partial<RecordAction> = {}): RecordAction[] {
  return [
    { key: 'detail', label: '查看详情', run: async () => null },
    { key: 'retry', label: '重试任务', run: async () => null },
    {
      key: 'delete',
      label: '删除订阅',
      danger: true,
      confirmLines: ['删除该订阅。', '保留已入库资源。'],
      confirmLabel: '确认删除',
      run: async () => null,
      ...overrides,
    },
  ];
}

describe('RecordActions menu', () => {
  it('opens on click, shows a trigger tooltip, and closes on Escape', async () => {
    render(<RecordActions actions={baseActions()} />);
    const trigger = screen.getByRole('button', { name: '更多操作' });
    expect(trigger).toHaveAttribute('title', '更多操作');
    expect(screen.queryByRole('menu')).toBeNull();

    await userEvent.click(trigger);
    const menu = await screen.findByRole('menu');
    expect(within(menu).getByRole('menuitem', { name: '查看详情' })).toBeInTheDocument();

    await userEvent.keyboard('{Escape}');
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('pins the dangerous action to the bottom after a separator', async () => {
    render(<RecordActions actions={baseActions()} />);
    await userEvent.click(screen.getByRole('button', { name: '更多操作' }));
    const menu = await screen.findByRole('menu');
    const items = within(menu).getAllByRole('menuitem');
    expect(items[items.length - 1]).toHaveTextContent('删除订阅');
    expect(within(menu).getByRole('separator')).toBeInTheDocument();
  });

  it('closes after selecting a non-confirm action and runs it', async () => {
    const run = vi.fn(async () => null);
    render(<RecordActions actions={[{ key: 'retry', label: '重试任务', run }]} />);
    await userEvent.click(screen.getByRole('button', { name: '更多操作' }));
    await userEvent.click(await screen.findByRole('menuitem', { name: '重试任务' }));
    expect(run).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('requires two-step confirmation for a dangerous action', async () => {
    const run = vi.fn(async () => null);
    const onChanged = vi.fn();
    render(<RecordActions actions={baseActions({ run })} onChanged={onChanged} />);
    await userEvent.click(screen.getByRole('button', { name: '更多操作' }));
    await userEvent.click(await screen.findByRole('menuitem', { name: '删除订阅' }));

    // First click only opens the confirmation; the action is not executed.
    expect(run).not.toHaveBeenCalled();
    expect(screen.getByRole('alertdialog')).toHaveTextContent('删除该订阅。');

    await userEvent.click(screen.getByRole('button', { name: '确认删除' }));
    expect(run).toHaveBeenCalledTimes(1);
    expect(onChanged).toHaveBeenCalledTimes(1);
  });

  it('keeps the record and shows the reason when the action fails', async () => {
    const run = vi.fn(async () => '后端返回的具体失败原因');
    const onChanged = vi.fn();
    render(<RecordActions actions={baseActions({ run })} onChanged={onChanged} />);
    await userEvent.click(screen.getByRole('button', { name: '更多操作' }));
    await userEvent.click(await screen.findByRole('menuitem', { name: '删除订阅' }));
    await userEvent.click(screen.getByRole('button', { name: '确认删除' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('后端返回的具体失败原因');
    expect(onChanged).not.toHaveBeenCalled();
  });

  it('does not trigger row click when toggling the menu', async () => {
    const onRowClick = vi.fn();
    render(
      <div onClick={onRowClick}>
        <RecordActions actions={baseActions()} />
      </div>,
    );
    await userEvent.click(screen.getByRole('button', { name: '更多操作' }));
    expect(onRowClick).not.toHaveBeenCalled();
  });

  it('disables actions not allowed by state', async () => {
    render(<RecordActions actions={[{ key: 'retry', label: '重试任务', disabled: true, run: async () => null }]} />);
    await userEvent.click(screen.getByRole('button', { name: '更多操作' }));
    const item = await screen.findByRole('menuitem', { name: '重试任务' });
    expect(item).toBeDisabled();
  });
});
