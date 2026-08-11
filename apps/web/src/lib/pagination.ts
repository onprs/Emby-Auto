import { useNavigate, useSearch } from '@tanstack/react-router';

type CursorSearch = {
  cursor?: string;
  cursorStack?: string;
};

const rootCursor = '_';

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
    update({
      cursor: nextCursor,
      cursorStack: [...stack, cursor ?? rootCursor].join(','),
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
