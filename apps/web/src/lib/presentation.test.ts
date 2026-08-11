import { describe, expect, it } from 'vitest';

import {
  acquisitionStageFilters,
  acquisitionStages,
  friendlyError,
  operationLabel,
  reasonLabel,
  resourceTypeLabel,
} from '@/lib/presentation';

describe('acquisitionStages', () => {
  it('uses natural Chinese stage names without raw backend keys', () => {
    expect(acquisitionStages.downloading.label).toBe('下载中');
    expect(acquisitionStages.processing.label).toBe('处理中');
    expect(acquisitionStages.awaiting_review.label).toBe('待审核');
    expect(acquisitionStages.importing.label).toBe('入库中');
    expect(acquisitionStages.completed.label).toBe('已入库');
    expect(acquisitionStages.failed.label).toBe('失败');
    for (const [key, stage] of Object.entries(acquisitionStages)) {
      expect(stage.label, `stage ${key} leaks a raw key`).not.toMatch(/[a-z]+_[a-z]+/);
    }
  });
});

describe('acquisitionStageFilters', () => {
  it('keeps filter labels consistent with stage labels', () => {
    const filters = Object.fromEntries(acquisitionStageFilters.map((filter) => [filter.value, filter.label]));
    expect(filters.downloading).toBe(acquisitionStages.downloading.label);
    expect(filters.processing).toBe(acquisitionStages.processing.label);
    expect(filters.awaiting_review).toBe(acquisitionStages.awaiting_review.label);
    expect(filters.importing).toBe(acquisitionStages.importing.label);
    expect(filters.completed).toBe(acquisitionStages.completed.label);
    expect(filters.attention).toBe('需要处理');
  });
});

describe('friendlyError', () => {
  it('explains current and historical subtitle pipeline failures without exposing backend text', () => {
    expect(friendlyError('simplified_chinese_subtitle_not_found')).toBe('没有找到可尝试的中文文本字幕。');
    expect(friendlyError('subtitle_candidates_exhausted')).toBe('已尝试所有可用字幕，但都无法生成有效的简体 ASS 字幕。');
    expect(friendlyError('ffmpeg_subtitle_candidates_failed')).toBe('字幕转换暂时失败，系统会自动重试其他候选。');
    expect(friendlyError('subtitle_output_invalid')).toBe('字幕转换结果校验失败，请检查字幕来源后重试。');
    expect(friendlyError('subtitle_content_inspection_failed')).toBe('暂时无法检查内封字幕内容，系统会自动重试。');
    expect(friendlyError('subtitle_content_inspection_limit')).toBe('内封字幕轨过多，无法安全判断简体字幕。');
  });
});

describe('reasonLabel', () => {
  it('explains RSS rejection and target occupancy reasons', () => {
    expect(reasonLabel('episode_ambiguous')).toBe('集数信息相互冲突');
    expect(reasonLabel('target_episode_in_library')).toBe('媒体库已有该集');
    expect(reasonLabel('target_episode_imported')).toBe('该集已由系统入库');
    expect(reasonLabel('target_episode_processing')).toBe('该集正在处理中');
  });
});

describe('operationLabel', () => {
  it('translates known operation kinds into Chinese', () => {
    expect(operationLabel('rss.poll')).toBe('检查 RSS 更新');
    expect(operationLabel('rss.subscription.complete')).toBe('完成 RSS 订阅');
    expect(operationLabel('transcode.run')).toBe('转换视频');
    expect(operationLabel('emby.import')).toBe('放入媒体库');
    expect(operationLabel('emby.refresh')).toBe('请求 Emby 扫描文件');
    expect(operationLabel('emby.scan')).toBe('从 Emby 更新目录');
  });

  it('never returns the raw kind for unknown values', () => {
    expect(operationLabel('some.unknown_kind')).toBe('后台处理');
  });
});

describe('resourceTypeLabel', () => {
  it('translates known resource types into Chinese', () => {
    expect(resourceTypeLabel('episode_task')).toBe('媒体任务');
    expect(resourceTypeLabel('rss_subscription')).toBe('RSS 订阅');
    expect(resourceTypeLabel('emby_scan')).toBe('目录更新记录');
  });

  it('falls back to a generic label instead of the raw key', () => {
    expect(resourceTypeLabel('unknown_type')).toBe('关联内容');
    expect(resourceTypeLabel(undefined)).toBe('关联内容');
    expect(resourceTypeLabel('')).toBe('关联内容');
  });
});
