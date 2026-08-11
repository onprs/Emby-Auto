import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { OverallProgressBar } from '@/components/overall-progress';

describe('OverallProgressBar', () => {
  it('keeps one decimal of real lifecycle progress instead of rounding the bar to whole stages', () => {
    render(<OverallProgressBar value={0.0228} label="等待下载" ariaLabel="任务整体进度" />);

    const progress = screen.getByRole('progressbar', { name: '任务整体进度' });
    expect(progress).toHaveAttribute('aria-valuenow', '2.3');
    expect(progress).toHaveAttribute('aria-valuetext', '2.3%');
    expect(screen.getByText('2.3%')).toBeInTheDocument();
    expect(progress.firstElementChild).toHaveStyle({ width: '2.3%' });
  });
});
