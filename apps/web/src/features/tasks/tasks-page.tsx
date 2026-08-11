import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useLocation, useNavigate, useSearch } from '@tanstack/react-router';
import { useState } from 'react';

import type { Task, TaskState } from '@/api/generated/types.gen';
import { fetchTasks } from '@/features/tasks/api';
import { cancelTaskCommand } from '@/features/tasks/api';
import { canDeleteTask, deleteTask, retryTask, runBatch, type TaskActionResult, type TaskActionTarget } from '@/features/tasks/task-actions';
import { taskFailureInfo } from '@/features/tasks/task-failure';
import { TaskFailureSummary } from '@/features/tasks/task-failure-summary';
import { appNavigationState, currentAppLocation, rememberListPosition, useListScrollRestoration } from '@/app/navigation-context';
import { TaskBatchBar } from '@/features/tasks/task-batch-bar';
import { IdempotencyKeyHolder } from '@/lib/idempotency';
import { DataTable, PageBody, PageHeader, PaginationControls } from '@/components/resource';
import { RecordActions, type RecordAction } from '@/components/record-actions';
import { CleanupStageBadge, StatusBadge } from '@/components/status-badge';
import { EmptyState, ErrorState, LoadingState } from '@/components/ui/feedback';
import { FilterChip } from '@/components/ui/filter-chip';
import { formatDateTime } from '@/lib/format';
import { useCursorPagination } from '@/lib/pagination';

const stateFilters: { value: TaskState | ''; label: string }[] = [
  { value: '', label: '全部' },
  { value: 'processing', label: '处理中' },
  { value: 'awaiting_review', label: '待审核' },
  { value: 'importing', label: '入库中' },
  { value: 'imported', label: '已入库' },
  { value: 'failed', label: '失败' },
];

export function TasksPage() {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const location = useLocation();
  const search = useSearch({ strict: false }) as {
    state?: TaskState;
    phase?: 'processing' | 'awaiting_review' | 'importing' | 'failed' | 'cleanup_failed';
  };
  const state = search.state ?? '';
  const pagination = useCursorPagination();
  const listSource = currentAppLocation(location.href);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [results, setResults] = useState<TaskActionResult[] | null>(null);
  const holder = useState(() => new IdempotencyKeyHolder())[0];

  const tasks = useQuery({
    queryKey: ['tasks', 'list', state, search.phase, pagination.cursor],
    queryFn: () => fetchTasks(pagination.cursor, state, search.phase),
  });

  const refresh = () => {
    setSelected(new Set());
    void queryClient.invalidateQueries({ queryKey: ['tasks'] });
    void queryClient.invalidateQueries({ queryKey: ['dashboard'] });
    void queryClient.invalidateQueries({ queryKey: ['acquisitions'] });
  };

  const items = tasks.data?.items ?? [];
  const selectedTasks = items.filter((task) => selected.has(task.id));
  const retryableTasks = selectedTasks.filter((task) => taskFailureInfo(task)?.canRetry);
  const deletableTasks = selectedTasks.filter(canDeleteTask);
  const allChecked = items.length > 0 && items.every((task) => selected.has(task.id));

  useListScrollRestoration(listSource, Boolean(tasks.data));

  const openTaskDetail = (taskId: string) => {
    rememberListPosition(listSource);
    void navigate({
      to: '/tasks/$taskId',
      params: { taskId },
      search: { from: listSource },
      state: appNavigationState(listSource),
    });
  };

  const toggleAll = () => {
    setSelected(allChecked ? new Set() : new Set(items.map((task) => task.id)));
  };
  const toggleOne = (id: string) => {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  };

  const execute = async (action: 'retry' | 'delete', targets: TaskActionTarget[]) => {
    setResults(null);
    const outcome = await runBatch(targets, action, () => holder.get());
    holder.reset();
    setResults(outcome);
    if (outcome.some((result) => result.ok)) {
      refresh();
    }
    return outcome;
  };

  const rowActions = (task: Task): RecordAction[] => {
    const target: TaskActionTarget = task;
    const failure = taskFailureInfo(task);
    const actions: RecordAction[] = [
      {
        key: 'detail',
        label: failure ? '查看失败原因' : '查看详情',
        run: async () => {
          openTaskDetail(task.id);
          return null;
        },
      },
    ];
    if (failure?.canRetry && failure.retryKind === 'task') {
      actions.push({
        key: 'retry',
        label: failure.retryLabel ?? '重试任务',
        title: '复用原任务配置重新执行，创建新的执行记录',
        run: async () => {
          const result = await retryTask(target, holder.get());
          holder.reset();
          if (result.ok) { refresh(); return null; }
          return result.error ?? '重试失败';
        },
      });
    }
    if (failure?.canRetry && failure.retryKind === 'cleanup') {
      actions.push({
        key: 'retry-cleanup',
        label: failure.retryLabel ?? '重试清理',
        title: '重新执行临时文件清理',
        run: async () => {
          const result = await retryTask(target, holder.get());
          holder.reset();
          if (result.ok) { refresh(); return null; }
          return result.error ?? '重试清理失败';
        },
      });
    }
    if (task.actions.canCancel) {
      actions.push({
        key: 'cancel',
        label: '取消任务',
        title: '请求所有排队中或运行中的处理操作停止',
        confirmLines: ['取消会请求所有排队中或运行中的处理操作停止，已产生的产物不会自动删除。'],
        confirmLabel: '确认取消',
        run: async () => {
          try {
            await cancelTaskCommand(task.id, holder.get(), task.version);
            holder.reset();
            refresh();
            return null;
          } catch (cause) {
            return cause instanceof Error ? cause.message : '取消失败';
          }
        },
      });
    }
    if (canDeleteTask(task)) {
      actions.push({
        key: 'delete',
        label: '删除任务',
        danger: true,
        confirmLines: [
          '停止这个任务正在进行的下载、转码或处理。',
          '删除尚未入库的下载源文件、种子任务和临时缓存。',
          '已经成功入库到 Emby 的正式资源不会被删除。',
        ],
        confirmLabel: '确认删除',
        run: async () => {
          const result = await deleteTask(target, holder.get());
          holder.reset();
          if (result.ok) { refresh(); return null; }
          return result.error ?? '删除失败';
        },
      });
    }
    return actions;
  };

  return (
    <PageBody>
      <PageHeader title="媒体处理" description="查看视频、字幕、审核和入库进度；可重试或删除任务" />

      <div className="mb-4 flex flex-wrap gap-2" role="group" aria-label="状态筛选">
        {stateFilters.map((filter) => (
          <FilterChip
            key={filter.label}
            to="/tasks"
            search={{
              state: filter.value || undefined,
              phase: undefined,
              cursor: undefined,
              cursorStack: undefined,
            }}
            active={state === filter.value}
            label={filter.label}
          />
        ))}
      </div>

      {selectedTasks.length > 0 ? (
        <TaskBatchBar
          count={selectedTasks.length}
          retryable={retryableTasks.length}
          deletable={deletableTasks.length}
          results={results}
          onAction={execute}
          onClear={() => { setSelected(new Set()); setResults(null); }}
          retryTargets={retryableTasks}
          deleteTargets={deletableTasks}
        />
      ) : results ? (
        <BatchOutcome results={results} onDismiss={() => setResults(null)} listSource={listSource} />
      ) : null}


      {tasks.isPending ? (
        <LoadingState label="正在读取任务" />
      ) : tasks.error ? (
        <ErrorState message={tasks.error.message} onRetry={() => tasks.refetch()} />
      ) : items.length === 0 ? (
        <EmptyState title="暂无任务" description="当前筛选条件下没有任务" />
      ) : (
        <>
          <div className="space-y-2 sm:hidden">
            {items.map((task) => {
              const failure = taskFailureInfo(task);
              return (
              <div key={task.id} className="rounded-xl border border-zinc-200/90 bg-white p-4 shadow-card transition-shadow duration-200 hover:shadow-card-hover">
                <div className="flex items-start gap-3">
                  <input
                    type="checkbox"
                    aria-label={`选择 ${taskTitle(task)}`}
                    checked={selected.has(task.id)}
                    onChange={() => toggleOne(task.id)}
                    className="mt-1 size-4 accent-emerald-700"
                  />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-start justify-between gap-3">
                      <div className="min-w-0">
                        <Link
                          to="/tasks/$taskId"
                          params={{ taskId: task.id }}
                          search={{ from: listSource }}
                          state={appNavigationState(listSource)}
                          onClick={() => rememberListPosition(listSource)}
                          className="block truncate font-medium text-zinc-900 hover:underline"
                        >
                          {taskTitle(task)}
                        </Link>
                        <p className="mt-1 text-sm text-zinc-500">{taskEpisode(task)}</p>
                      </div>
                      <StatusBadge value={task.state} />
                    </div>
                    <div className="mt-2 flex items-center gap-2 text-xs text-zinc-500">
                      {task.state === 'imported' && task.cleanup ? <CleanupStageBadge value={task.cleanup.status} /> : null}
                      <span>{formatDateTime(task.updatedAt)}</span>
                    </div>
                    {failure ? <TaskFailureSummary info={failure} className="mt-2 text-sm" /> : null}
                    <div className="mt-3 flex justify-end">
                      <RecordActions actions={rowActions(task)} onChanged={refresh} />
                    </div>
                  </div>
                </div>
              </div>
              );
            })}
          </div>
          <div className="hidden sm:block">
            <DataTable head={[
              <input key="all" type="checkbox" aria-label="全选" checked={allChecked} onChange={toggleAll} className="size-4 accent-emerald-700" />,
              '内容', '集数', '当前进度', '清理', '最近更新', '操作',
            ]}>
              {items.map((task) => {
                const failure = taskFailureInfo(task);
                return (
                <tr key={task.id}>
                  <td className="px-4 py-3">
                    <input
                      type="checkbox"
                      aria-label={`选择 ${taskTitle(task)}`}
                      checked={selected.has(task.id)}
                      onChange={() => toggleOne(task.id)}
                      className="size-4 accent-emerald-700"
                    />
                  </td>
                  <td className="max-w-0 px-4 py-3">
                    <Link
                      to="/tasks/$taskId"
                      params={{ taskId: task.id }}
                      search={{ from: listSource }}
                      state={appNavigationState(listSource)}
                      onClick={() => rememberListPosition(listSource)}
                      className="block truncate font-medium text-zinc-900 hover:underline"
                    >
                      {taskTitle(task)}
                    </Link>
                  </td>
                  <td className="px-4 py-3 text-zinc-600">{taskEpisode(task)}</td>
                  <td className="px-4 py-3">
                    <StatusBadge value={task.state} />
                    {failure ? <TaskFailureSummary info={failure} className="mt-1 max-w-md text-xs" /> : null}
                  </td>
                  <td className="px-4 py-3">
                    {task.state === 'imported' && task.cleanup ? <CleanupStageBadge value={task.cleanup.status} /> : <span className="text-zinc-400">—</span>}
                  </td>
                  <td className="px-4 py-3 text-zinc-600">{formatDateTime(task.updatedAt)}</td>
                  <td className="w-12 px-2 py-3 text-right">
                    <RecordActions actions={rowActions(task)} onChanged={refresh} />
                  </td>
                </tr>
                );
              })}
            </DataTable>
          </div>
          <PaginationControls
            canGoBack={pagination.canGoBack}
            hasNext={Boolean(tasks.data?.nextCursor)}
            onPrevious={pagination.goPrevious}
            onNext={() => pagination.goNext(tasks.data?.nextCursor)}
            isFetching={tasks.isFetching}
          />
        </>
      )}
    </PageBody>
  );
}

function taskTitle(task: Task): string {
  return task.mediaType === 'movie'
    ? `${task.movieTitle ?? '未命名电影'}${task.releaseYear ? ` (${task.releaseYear})` : ''}`
    : (task.seriesTitle ?? '未命名番剧');
}

function taskEpisode(task: Task): string {
  return task.mediaType === 'movie'
    ? '电影'
    : `S${String(task.targetSeason).padStart(2, '0')}E${String(task.targetEpisode).padStart(2, '0')}`;
}

function BatchOutcome({ results, onDismiss, listSource }: { results: TaskActionResult[]; onDismiss: () => void; listSource: string }) {
  const failed = results.filter((result) => !result.ok);
  if (failed.length === 0) {
    return (
      <div className="mb-4 flex animate-scale-in items-center justify-between rounded-xl border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-800" role="status">
        全部 {results.length} 项操作成功。
        <button type="button" className="font-medium hover:underline" onClick={onDismiss}>知道了</button>
      </div>
    );
  }
  return (
    <div className="mb-4 animate-scale-in rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-800" role="alert">
      <p>{results.length - failed.length} 项成功，{failed.length} 项失败：</p>
      <ul className="mt-1 list-inside list-disc">
        {failed.map((result) => (
          <li key={result.taskId}>
            <Link
              to="/tasks/$taskId"
              params={{ taskId: result.taskId }}
              search={{ from: listSource }}
              state={appNavigationState(listSource)}
              onClick={() => rememberListPosition(listSource)}
              className="font-medium hover:underline"
            >查看任务</Link>
            <span className="ml-2">{result.error}</span>
          </li>
        ))}
      </ul>
      <button type="button" className="mt-2 font-medium hover:underline" onClick={onDismiss}>关闭</button>
    </div>
  );
}
