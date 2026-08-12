/** Formatting helpers: local-timezone dates, byte sizes, unknown enums. */

export function formatDateTime(value: string | null | undefined): string {
  if (!value) {
    return '—';
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return date.toLocaleString('zh-CN', { hour12: false });
}

export function formatDate(value: string | null | undefined): string {
  if (!value) {
    return '—';
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return date.toLocaleDateString('zh-CN');
}

const BYTE_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB'] as const;

export function formatBytes(value: number | null | undefined): string {
  if (value === null || value === undefined || Number.isNaN(value)) {
    return '—';
  }
  if (value < 0) {
    return '—';
  }
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < BYTE_UNITS.length - 1) {
    size /= 1024;
    unit += 1;
  }
  const digits = size >= 100 || unit === 0 ? 0 : 1;
  return `${size.toFixed(digits)} ${BYTE_UNITS[unit]}`;
}

export function formatPercent(value: number | null | undefined): string {
  if (value === null || value === undefined || Number.isNaN(value)) {
    return '—';
  }
  return `${Math.round(value * 100)}%`;
}

/** Formats the elapsed time between two ISO timestamps in a compact Chinese form. */
export function formatDuration(start: string | null | undefined, end: string | null | undefined): string {
  if (!start || !end) {
    return '—';
  }
  const startTime = new Date(start).getTime();
  const endTime = new Date(end).getTime();
  if (Number.isNaN(startTime) || Number.isNaN(endTime) || endTime < startTime) {
    return '—';
  }
  const totalSeconds = Math.round((endTime - startTime) / 1000);
  if (totalSeconds < 60) {
    return `${totalSeconds} 秒`;
  }
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes < 60) {
    return seconds === 0 ? `${minutes} 分钟` : `${minutes} 分 ${seconds} 秒`;
  }
  const hours = Math.floor(minutes / 60);
  const restMinutes = minutes % 60;
  return restMinutes === 0 ? `${hours} 小时` : `${hours} 小时 ${restMinutes} 分`;
}

/** Renders an enum value, surfacing unknown values instead of crashing. */
export function formatEnum(value: string | null | undefined): string {
  if (value === null || value === undefined || value === '') {
    return '—';
  }
  return value;
}
