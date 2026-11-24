import { useNavigate, useSearch } from '@tanstack/react-router';

type CursorSearch = {
  cursor?: string;
  cursorStack?: string;
  cursorTruncated?: 'truncated';
};

const rootCursor = '_';
/**
 * URL 只保留最近若干页的游标：既能保证 URL 不触发路由的 4096 字符上限，
 * 也能在浏览器刷新/前进后退时恢复当前页。
 */
const maxCursorStackDepth = 50;

/** Cursor pagination persisted in URL search so refresh and history restore it. */
export function useCursorPagination() {
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as CursorSearch;
  const cursor = search.cursor;
  const stack = search.cursorStack?.split(',').filter(Boolean) ?? [];
  // 标记只在仍有游标时生效；筛选切换清空游标后，旧标记不再展示。
  const historyTruncated = search.cursorTruncated === 'truncated' && Boolean(cursor);

  const update = (next: CursorSearch) => {
    void navigate({
      // The hook is shared by routes with different validated search schemas.
      search: ((previous: Record<string, unknown>) => ({ ...previous, ...next })) as never,
    });
  };

  const goNext = (nextCursor: string | null | undefined) => {
    if (!nextCursor) {
      return;
    }
    const nextStack = [...stack, cursor ?? rootCursor];
    // 超限时保留根游标和最近的记录，并由 cursorTruncated 表示历史已被截断。
    const cursorStack = nextStack.length > maxCursorStackDepth
      ? [rootCursor, ...nextStack.slice(-(maxCursorStackDepth - 1))]
      : nextStack;
    update({
      cursor: nextCursor,
      cursorStack: cursorStack.join(','),
      cursorTruncated: cursorStack.length < nextStack.length || historyTruncated ? 'truncated' : undefined,
    });
  };

  const goPrevious = () => {
    if (stack.length === 0) {
      return;
    }
    // 最老保留游标处不再用“上一页”跨段跳到第一页；由独立的“回到第一页”入口处理。
    if (stack.length === 1 && historyTruncated) {
      return;
    }
    const previousStack = [...stack];
    const prior = previousStack.pop();
    update({
      cursor: prior && prior !== rootCursor ? prior : undefined,
      cursorStack: previousStack.length > 0 ? previousStack.join(',') : undefined,
      cursorTruncated: previousStack.length > 0 && historyTruncated ? 'truncated' : undefined,
    });
  };

  const reset = () => update({ cursor: undefined, cursorStack: undefined, cursorTruncated: undefined });

  return {
    cursor,
    pageIndex: stack.length,
    canGoBack: stack.length > 1 || (stack.length === 1 && !historyTruncated),
    canGoFirst: Boolean(cursor),
    historyTruncated,
    goNext,
    goPrevious,
    reset,
  };
}
