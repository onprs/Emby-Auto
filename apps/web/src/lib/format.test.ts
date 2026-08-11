import { describe, expect, it } from 'vitest';

import { formatBytes, formatDuration, formatEnum, formatPercent } from '@/lib/format';

describe('formatBytes', () => {
  it('formats bytes across units', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(2048)).toBe('2.0 KiB');
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.0 MiB');
  });

  it('handles missing and invalid values', () => {
    expect(formatBytes(undefined)).toBe('—');
    expect(formatBytes(null)).toBe('—');
    expect(formatBytes(Number.NaN)).toBe('—');
  });
});

describe('formatPercent', () => {
  it('formats a ratio as a percentage', () => {
    expect(formatPercent(0.5)).toBe('50%');
    expect(formatPercent(1)).toBe('100%');
  });

  it('handles missing values', () => {
    expect(formatPercent(undefined)).toBe('—');
  });
});

describe('formatDuration', () => {
  it('formats seconds, minutes and hours', () => {
    expect(formatDuration('2026-07-25T10:00:00Z', '2026-07-25T10:00:45Z')).toBe('45 秒');
    expect(formatDuration('2026-07-25T10:00:00Z', '2026-07-25T10:03:00Z')).toBe('3 分钟');
    expect(formatDuration('2026-07-25T10:00:00Z', '2026-07-25T10:03:30Z')).toBe('3 分 30 秒');
    expect(formatDuration('2026-07-25T10:00:00Z', '2026-07-25T12:00:00Z')).toBe('2 小时');
    expect(formatDuration('2026-07-25T10:00:00Z', '2026-07-25T11:05:00Z')).toBe('1 小时 5 分');
  });

  it('handles missing, invalid or reversed timestamps', () => {
    expect(formatDuration(undefined, '2026-07-25T10:00:00Z')).toBe('—');
    expect(formatDuration('2026-07-25T10:00:00Z', undefined)).toBe('—');
    expect(formatDuration('not-a-date', '2026-07-25T10:00:00Z')).toBe('—');
    expect(formatDuration('2026-07-25T11:00:00Z', '2026-07-25T10:00:00Z')).toBe('—');
  });
});

describe('formatEnum', () => {
  it('returns the raw value for unknown enums instead of crashing', () => {
    expect(formatEnum('some_future_state')).toBe('some_future_state');
  });

  it('handles empty values', () => {
    expect(formatEnum('')).toBe('—');
    expect(formatEnum(undefined)).toBe('—');
  });
});
