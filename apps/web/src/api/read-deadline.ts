const READ_TIMEOUT_MS = 20_000;

/** 保留调用方的取消信号，并让普通读取的响应头和响应体共用一个期限。 */
export function withReadDeadline(request: Request, timeoutMs = READ_TIMEOUT_MS): Request {
  const path = new URL(request.url).pathname;
  if (
    !['GET', 'HEAD'].includes(request.method) ||
    !path.startsWith('/api/v1/') ||
    path === '/api/v1/events' ||
    /^\/api\/v1\/tasks\/[^/]+\/artifacts\//.test(path)
  ) {
    return request;
  }
  return new Request(request, {
    signal: AbortSignal.any([request.signal, AbortSignal.timeout(timeoutMs)]),
  });
}
