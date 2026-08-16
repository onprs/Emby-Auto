import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach, beforeAll, afterAll, vi } from 'vitest';

import { client } from '@/api/generated/client.gen';
import { server } from '@/test/msw-server';

// jsdom 未实现 scrollIntoView；下拉菜单打开后会调用它以显示选中项。
Element.prototype.scrollIntoView = vi.fn();

// Node fetch needs an absolute origin; the browser uses same-origin relative URLs.
client.setConfig({ baseUrl: 'http://localhost', credentials: 'include' });

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => {
  cleanup();
  server.resetHandlers();
});
afterAll(() => server.close());
