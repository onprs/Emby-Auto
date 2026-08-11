import { beforeEach, describe, expect, it } from 'vitest';

import {
  defaultTaskListSource,
  lastTaskListSource,
  normalizeTaskListSource,
  rememberTaskListPosition,
  restoreTaskListPosition,
  taskListHistoryDepth,
  taskListHistoryState,
} from '@/features/tasks/task-navigation';

describe('task list return navigation', () => {
  beforeEach(() => sessionStorage.clear());

  it('keeps filters and cursor pagination in a valid source URL', () => {
    expect(normalizeTaskListSource('/tasks?state=failed&cursor=11111111-1111-1111-1111-111111111111&cursorStack=_'))
      .toBe('/tasks?state=failed&cursor=11111111-1111-1111-1111-111111111111&cursorStack=_');
    expect(normalizeTaskListSource('/acquisitions?phase=attention')).toBe('/acquisitions?phase=attention');
  });

  it('rejects external, protocol-relative, and lookalike return URLs', () => {
    expect(normalizeTaskListSource('https://example.com/tasks?state=failed')).toBeUndefined();
    expect(normalizeTaskListSource('//example.com/tasks')).toBeUndefined();
    expect(normalizeTaskListSource('/tasks/11111111-1111-1111-1111-111111111111')).toBeUndefined();
    expect(normalizeTaskListSource('/acquisitions/11111111-1111-1111-1111-111111111111')).toBeUndefined();
  });

  it('falls back to the user-facing task list for direct detail links', () => {
    expect(defaultTaskListSource).toBe('/acquisitions');
    expect(lastTaskListSource()).toBe('/acquisitions');
  });

  it('accepts history depth only when it belongs to the same list source', () => {
    const state = taskListHistoryState('/acquisitions?phase=attention', 2);
    expect(taskListHistoryDepth(state, '/acquisitions?phase=attention')).toBe(2);
    expect(taskListHistoryDepth(state, '/tasks?state=failed')).toBeUndefined();
  });

  it('stores the exact list URL and restores its scroll position once', () => {
    const source = '/tasks?state=failed&cursor=11111111-1111-1111-1111-111111111111';
    rememberTaskListPosition(source, 864);

    expect(lastTaskListSource()).toBe(source);
    expect(restoreTaskListPosition(source)).toBe(864);
    expect(restoreTaskListPosition(source)).toBeUndefined();
  });
});
