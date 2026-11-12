import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import { ActionFeedbackAlerts, ActionFeedbackProvider } from '@/components/action-feedback';
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

function renderActions(actions: RecordAction[], onChanged?: () => void) {
  return render(
    <ActionFeedbackProvider>
      <RecordActions actions={actions} onChanged={onChanged} />
      <ActionFeedbackAlerts />
    </ActionFeedbackProvider>,
  );
}

async function failDangerousAction(rowIndex = 0) {
  await userEvent.click(screen.getAllByRole('button', { name: '更多操作' })[rowIndex]);
  await userEvent.click(await screen.findByRole('menuitem', { name: '删除订阅' }));
  await userEvent.click(screen.getByRole('button', { name: '确认删除' }));
}

describe('RecordActions menu', () => {
  it('opens on click, shows a trigger tooltip, and closes on Escape', async () => {
    renderActions(baseActions());
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
    renderActions(baseActions());
    await userEvent.click(screen.getByRole('button', { name: '更多操作' }));
    const menu = await screen.findByRole('menu');
    const items = within(menu).getAllByRole('menuitem');
    expect(items[items.length - 1]).toHaveTextContent('删除订阅');
    expect(within(menu).getByRole('separator')).toBeInTheDocument();
  });

  it('closes after selecting a non-confirm action and runs it', async () => {
    const run = vi.fn(async () => null);
    renderActions([{ key: 'retry', label: '重试任务', run }]);
    await userEvent.click(screen.getByRole('button', { name: '更多操作' }));
    await userEvent.click(await screen.findByRole('menuitem', { name: '重试任务' }));
    expect(run).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('requires two-step confirmation for a dangerous action', async () => {
    const run = vi.fn(async () => null);
    const onChanged = vi.fn();
    renderActions(baseActions({ run }), onChanged);
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
    renderActions(baseActions({ run }), onChanged);
    await failDangerousAction();

    // 失败后关闭确认框，提示不再被遮罩压住；再次执行必须重新确认。
    expect(await screen.findByRole('alert')).toHaveTextContent('后端返回的具体失败原因');
    expect(screen.queryByRole('alertdialog')).toBeNull();
    expect(onChanged).toHaveBeenCalledTimes(1);
  });

  it('closes the confirmation when the action throws', async () => {
    const run = vi.fn(async () => {
      throw new Error('网络中断，请稍后重试');
    });
    renderActions(baseActions({ run }));
    await failDangerousAction();

    expect(await screen.findByRole('alert')).toHaveTextContent('网络中断，请稍后重试');
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });

  it('stacks failures from multiple rows in one feedback region and keeps both readable', async () => {
    render(
      <ActionFeedbackProvider>
        <div>
          <RecordActions actions={baseActions({ run: async () => '第一行失败原因' })} />
          <RecordActions actions={baseActions({ run: async () => '第二行失败原因' })} />
        </div>
        <ActionFeedbackAlerts />
      </ActionFeedbackProvider>,
    );

    await failDangerousAction(0);
    expect(await screen.findAllByRole('alert')).toHaveLength(1);
    await failDangerousAction(1);

    const alerts = await screen.findAllByRole('alert');
    expect(alerts.map((alert) => alert.textContent)).toEqual(['第一行失败原因', '第二行失败原因']);
    for (const alert of alerts) {
      expect(within(alert).getByRole('button', { name: '关闭' })).toBeInTheDocument();
    }

    // 关闭其中一条不会影响另一条。
    await userEvent.click(within(alerts[0]).getByRole('button', { name: '关闭' }));
    expect(screen.getAllByRole('alert').map((alert) => alert.textContent)).toEqual(['第二行失败原因']);
  });

  it('replaces the same row failure instead of adding another alert', async () => {
    const run = vi.fn(async () => '第一次失败');
    renderActions(baseActions({ run }));
    await failDangerousAction();
    expect(await screen.findByRole('alert')).toHaveTextContent('第一次失败');

    run.mockResolvedValueOnce('第二次失败');
    await failDangerousAction();
    const alerts = await screen.findAllByRole('alert');
    expect(alerts).toHaveLength(1);
    expect(alerts[0]).toHaveTextContent('第二次失败');
  });

  it('does not trigger row click when toggling the menu', async () => {
    const onRowClick = vi.fn();
    render(
      <ActionFeedbackProvider>
        <div onClick={onRowClick}>
          <RecordActions actions={baseActions()} />
        </div>
        <ActionFeedbackAlerts />
      </ActionFeedbackProvider>,
    );
    await userEvent.click(screen.getByRole('button', { name: '更多操作' }));
    expect(onRowClick).not.toHaveBeenCalled();
  });

  it('disables actions not allowed by state', async () => {
    renderActions([{ key: 'retry', label: '重试任务', disabled: true, run: async () => null }]);
    await userEvent.click(screen.getByRole('button', { name: '更多操作' }));
    const item = await screen.findByRole('menuitem', { name: '重试任务' });
    expect(item).toBeDisabled();
  });
});
