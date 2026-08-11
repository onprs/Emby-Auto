import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach, beforeAll, afterAll } from 'vitest';

import { client } from '@/api/generated/client.gen';
import { server } from '@/test/msw-server';

// Node fetch needs an absolute origin; the browser uses same-origin relative URLs.
client.setConfig({ baseUrl: 'http://localhost', credentials: 'include' });

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => {
  cleanup();
  server.resetHandlers();
});
afterAll(() => server.close());
