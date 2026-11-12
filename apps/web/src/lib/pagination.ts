import { useNavigate, useSearch } from '@tanstack/react-router';

type CursorSearch = {
  cursor?: string;
  cursorStack?: string;
};

const rootCursor = '_';
/**
 * URL 只保留最近若干页的游标：既能保证“上一页”始终可用，
 * 也不会让 cursorStack 超过路由校验的 4096 字符上限。
 */
const maxCursorStackDepth = 50;

/** Cursor pagination persisted in URL search so refresh and history restore it. */
export function useCursorPagination() {
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as CursorSearch;
  const cursor = search.cursor;
  const stack = search.cursorStack?.split(',').filter(Boolean) ?? [];

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
    // 超限时保留根游标和最近的记录，深页可以逐页退回第一页，而不是整段栈被丢弃。
    const cursorStack = nextStack.length > maxCursorStackDepth
      ? [rootCursor, ...nextStack.slice(-(maxCursorStackDepth - 1))]
      : nextStack;
    update({
      cursor: nextCursor,
      cursorStack: cursorStack.join(','),
    });
  };

  const goPrevious = () => {
    const previousStack = [...stack];
    const prior = previousStack.pop();
    update({
      cursor: prior && prior !== rootCursor ? prior : undefined,
      cursorStack: previousStack.length > 0 ? previousStack.join(',') : undefined,
    });
  };

  const reset = () => update({ cursor: undefined, cursorStack: undefined });

  return {
    cursor,
    pageIndex: stack.length,
    canGoBack: stack.length > 0,
    goNext,
    goPrevious,
    reset,
  };
}
