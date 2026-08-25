const operationLabels: Record<string, string> = {
  'search.run': '搜索资源',
  'rss.poll': '检查 RSS 更新',
  'download.enqueue': '添加下载',
  'download.selection.apply': '应用文件解析',
  'agent.resolve': '自动解析内容',
  'download.sync': '同步下载进度',
  'download.materialize': '准备媒体处理',
  'download.cancel': '删除下载',
  'subtitle.prepare': '准备中文字幕',
  'transcode.run': '转换视频',
  'media.finalize': '检查处理结果',
  'emby.import': '放入媒体库',
  'cleanup.run': '清理临时文件',
  'emby.refresh': '请求 Emby 扫描文件',
  'emby.scan': '从 Emby 更新目录',
  'tmdb.sync': '同步作品信息',
  'task.cancel': '停止媒体处理',
  'acquisition.delete': '删除任务',
  'rss.subscription.complete': '完成 RSS 订阅',
  'rss.subscription.delete': '删除 RSS 订阅',
};

const errorMessages: Record<string, string> = {
  duplicate_torrent: '这个资源已经在下载列表中。',
  download_file_resolution_required: '下载文件需要确认后才能开始。',
  download_file_resolution_invalid: '下载文件解析结果不完整。',
  agent_disabled: 'Agent 辅助当前已关闭。',
  agent_capability_disabled: '此 Agent 辅助能力当前已关闭。',
  agent_resolution_stale: '资源或配置已变化，请重新生成建议。',
  agent_rate_limited: 'Agent 服务请求过多，系统会自动重试。',
  agent_request_timeout: 'Agent 服务响应超时。',
  agent_authentication_failed: 'Agent API 凭据无效。',
  agent_model_unavailable: 'Agent 模型不可用。',
  mapping_profile_required: '还没有设置剧集对应关系。',
  episode_mapping_required: '当前文件还没有对应到正确的剧集。',
  mapping_files_unavailable: '没有找到可处理的视频文件。',
  mapping_source_invalid: '无法从资源文件识别源季集数。',
  mapping_source_duplicate: '多个视频被识别成了相同的源季集数。',
  mapping_source_out_of_range: '资源集数超出了可映射范围。',
  mapping_context_incomplete: '现有映射资料不完整，请重新选择一个资源集与 TMDb 剧集。',
  mapping_anchor_source_invalid: '所选资源文件不在当前下载的视频文件中。',
  mapping_anchor_target_invalid: '所选 TMDb 剧集不能作为常规剧集映射锚点。',
  mapping_anchor_invalid: '所选剧集对应关系已经不可用，请重新选择。',
  mapping_source_season_mismatch: '当前资源包含不同的源季，无法使用同一个集数锚点自动映射。',
  mapping_catalog_invalid: 'TMDb 剧集资料无法生成自动映射，请先更新剧集信息。',
  mapping_catalog_incomplete: 'TMDb 剧集资料不完整，请先更新剧集信息。',
  mapping_target_out_of_range: '按所选对应关系推导后超出了 TMDb 剧集范围。',
  mapping_title_missing: '目标剧集缺少 TMDb 标题，请先更新剧集信息。',
  mapping_incomplete: '仍有视频没有对应到 TMDb 剧集，映射未保存。',
  mapping_scope_incomplete: '请为每个已选视频选择映射目标或明确排除。',
  mapping_source_scope_violation: '资源文件已经变化，请刷新后重新设置映射。',
  mapping_source_changed: '资源文件的季集识别已经变化，请刷新后重试。',
  mapping_target_duplicate: '多个源视频不能映射到同一个 TMDb 剧集。',
  mapping_target_invalid: '所选 TMDb 剧集不存在，请更新剧集信息后重试。',
  mapping_target_occupied: '所选 TMDb 剧集已经存在或正在处理中。',
  mapping_explicit_empty: '逐个文件映射至少需要保留一个视频。',
  simplified_chinese_subtitle_not_found: '没有找到可尝试的中文文本字幕。',
  subtitle_candidates_exhausted: '已尝试所有可用字幕，但都无法生成有效的简体 ASS 字幕。',
  ffmpeg_subtitle_candidates_failed: '字幕转换暂时失败，系统会自动重试其他候选。',
  subtitle_output_invalid: '字幕转换结果校验失败，请检查字幕来源后重试。',
  subtitle_content_inspection_failed: '暂时无法检查内封字幕内容，系统会自动重试。',
  subtitle_content_inspection_limit: '内封字幕轨过多，无法安全判断简体字幕。',
  subtitle_format_unsupported: '找到的中文字幕格式暂不支持。',
  transcode_output_invalid: '视频转换结果校验失败，请检查 FFmpeg 设置后重试。',
  download_in_use: '这个下载仍在处理中，请先停止相关任务。',
  qbittorrent_unavailable: '暂时无法连接 qBittorrent，请检查服务是否运行。',
  qbittorrent_enqueue_failed: '没有成功把资源添加到 qBittorrent。',
  qbittorrent_files_unavailable: '暂时无法读取下载文件列表。',
  qbittorrent_rate_limit_failed: '下载已添加，但没有成功应用 qBittorrent 速率限制。',
  qbittorrent_category_failed: '下载已添加，但整理 qBittorrent 分类失败。',
  qbittorrent_file_priority_failed: '没有成功保存要下载的文件。',
  qbittorrent_resume_failed: '资源已经添加，但没有成功开始下载。',
  download_no_main_video: '资源中没有找到可处理的正片视频。',
  download_file_selection_invalid: '无法安全选择这个资源中的媒体文件。',
  rss_fetch_failed: '暂时无法读取 RSS 地址，系统会自动重试。',
  search_provider_failed: '搜索来源暂时不可用，请稍后重试。',
  configuration_unavailable: '应用设置暂时不可用。',
  tmdb_catalog_missing: '作品信息还没有同步完成。',
  operation_interrupted: '处理被中断，系统会自动重试。',
  state_conflict: '任务状态已经变化，请刷新后再试。',
  rss_subscription_completed: '订阅已经完成，不能重新启用或继续检查。',
  invalid_state: '当前任务状态不允许执行这个操作，请刷新后查看最新状态。',
  idempotency_conflict: '同一操作正在处理中，请等待状态更新。',
  service_unavailable: '后端服务暂时不可用，请稍后再试。',
  acquisition_delete_path_unsafe: '待删除文件不在允许的临时目录内，请检查路径设置。',
  acquisition_delete_roots_not_configured: '临时文件目录尚未配置完整。',
  acquisition_delete_file_failed: '暂时无法删除源文件或临时文件，请检查文件是否正在使用。',
  acquisition_delete_storage_unavailable: '暂时无法完成任务记录清理，请稍后重试。',
  torrent_source_invalid: '下载链接无效，请检查订阅地址。',
  torrent_source_unavailable: '暂时无法获取种子文件，请稍后重试。',
  torrent_source_too_large: '种子文件过大，无法处理。',
  torrent_source_not_torrent: '种子文件无效。',
  qbittorrent_invalid_torrent: '种子文件无效，qBittorrent 无法识别。',
  network_proxy_not_configured: '请先在设置中启用并验证网络代理。',
  qbittorrent_delete_failed: '暂时无法从 qBittorrent 移除下载，请稍后重试。',
};

const rejectionLabels: Record<string, string> = {
  episode_range_batch: '合集或多集资源',
  non_episode_extra: '花絮或非正片',
  episode_not_detected: '无法识别集数',
  agent_ignored: '自动筛选忽略',
  episode_ambiguous: '集数信息相互冲突',
  download_uri_missing: '没有可用下载地址',
  title_include_mismatch: '未命中包含词',
  title_excluded: '命中不包含词',
  target_episode_in_library: '媒体库已有该集',
  target_episode_imported: '该集已由系统入库',
  target_episode_processing: '该集正在处理中',
  extra_content: '花絮或附加内容',
  unsupported_media: '不支持的文件类型',
  alternate_video: '备用视频',
  alternate_subtitle: '备用字幕',
  no_matching_video: '没有对应视频',
  not_selected: '未选择',
};

const clientStateLabels: Record<string, string> = {
  added: '已添加',
  downloading: '正在下载',
  stalledDL: '等待下载速度',
  metaDL: '正在读取资源信息',
  forcedDL: '正在下载',
  queuedDL: '等待下载',
  pausedDL: '已暂停',
  stoppedDL: '已暂停',
  uploading: '下载完成',
  stalledUP: '下载完成',
  forcedUP: '下载完成',
  queuedUP: '下载完成',
  pausedUP: '下载完成',
  checkingDL: '正在校验文件',
  checkingUP: '正在校验文件',
  error: '下载出错',
  missingFiles: '文件已丢失',
};

const resourceTypeLabels: Record<string, string> = {
  acquisition: '内容',
  download: '下载',
  episode_task: '媒体任务',
  media_series: '作品',
  rss_subscription: 'RSS 订阅',
  search_run: '搜索',
  emby_scan: '目录更新记录',
  emby_catalog: '媒体库',
};

export type StageTone = 'neutral' | 'success' | 'warning' | 'danger' | 'info';

/** User-facing cleanup states for the temporary-file cleanup lifecycle. */
export const cleanupStages: Record<string, { label: string; tone: StageTone }> = {
  queued: { label: '等待清理', tone: 'info' },
  running: { label: '正在清理', tone: 'info' },
  completed: { label: '清理完成', tone: 'success' },
  failed: { label: '清理失败', tone: 'danger' },
  cancelled: { label: '已取消清理', tone: 'neutral' },
};

export function cleanupStageLabel(status?: string | null): string {
  if (!status) return '未清理';
  return cleanupStages[status]?.label ?? '状态已更新';
}

/** User-facing task stages for the acquisitions pipeline. */
export const acquisitionStages: Record<string, { label: string; tone: StageTone }> = {
  pending: { label: '等待下载', tone: 'info' },
  downloading: { label: '下载中', tone: 'info' },
  mapping_pending: { label: '待设置集数', tone: 'warning' },
  materializing: { label: '准备文件', tone: 'info' },
  processing: { label: '处理中', tone: 'info' },
  awaiting_review: { label: '待审核', tone: 'warning' },
  importing: { label: '入库中', tone: 'info' },
  completed: { label: '已入库', tone: 'success' },
  failed: { label: '失败', tone: 'danger' },
  rejected: { label: '审核未通过', tone: 'danger' },
  cancelled: { label: '已取消', tone: 'neutral' },
};

export const acquisitionPipelineStageLabels = {
  source: '来源导入',
  download: '下载',
  mapping: '剧集映射',
  transcode: '视频转码',
  subtitle: '字幕处理',
  rename: '文件重命名',
  organize: '目录标准化',
  review: '人工审核',
  import: 'Emby 入库',
} as const;

export const acquisitionPipelineStageStatuses: Record<string, { label: string; tone: StageTone }> = {
  pending: { label: '等待中', tone: 'neutral' },
  blocked: { label: '前置阶段未完成', tone: 'warning' },
  running: { label: '处理中', tone: 'info' },
  waiting: { label: '等待确认', tone: 'warning' },
  completed: { label: '已完成', tone: 'success' },
  failed: { label: '失败', tone: 'danger' },
  rejected: { label: '未通过', tone: 'danger' },
  cancelled: { label: '已取消', tone: 'neutral' },
  skipped: { label: '无需处理', tone: 'neutral' },
};

/** Stage filters shown on the task list; labels must match acquisitionStages. */
export const acquisitionStageFilters = [
  { value: '', label: '全部' },
  { value: 'downloading', label: '下载中' },
  { value: 'processing', label: '处理中' },
  { value: 'awaiting_review', label: '待审核' },
  { value: 'importing', label: '入库中' },
  { value: 'completed', label: '已入库' },
  { value: 'attention', label: '需要处理' },
] as const;

export function operationLabel(kind: string): string {
  return operationLabels[kind] ?? '后台处理';
}

export function resourceTypeLabel(resourceType?: string | null): string {
  if (!resourceType) return '关联内容';
  return resourceTypeLabels[resourceType] ?? '关联内容';
}

export function friendlyError(code?: string | null, fallback?: string | null): string {
  if (code && errorMessages[code]) return errorMessages[code];
  if (fallback && /[\u3400-\u9fff]/.test(fallback)) return fallback;
  return '处理没有成功，请打开诊断信息查看详情。';
}

export function decisionSourceLabel(source?: string | null): string {
  switch (source) {
    case 'deterministic': return '规则解析';
    case 'agent_auto': return '自动解析';
    case 'agent_accepted': return '自动解析（已确认）';
    case 'user': return '人工设置';
    case 'legacy': return '历史设置';
    default: return '未确认';
  }
}

export function failureStageLabel(stage?: string | null): string {
  switch (stage) {
    case 'enqueue': return '添加下载';
    case 'file_resolution': return '解析下载文件';
    case 'sync': return '同步下载进度';
    case 'materialize': return '准备媒体处理';
    case 'video': return '转换视频';
    case 'subtitle': return '准备字幕';
    case 'finalize': return '检查处理结果';
    case 'import': return '放入媒体库';
    case 'cleanup': return '清理临时文件';
    default: return '处理';
  }
}

export function clientStateLabel(state?: string | null): string {
  if (!state) return '等待同步';
  return clientStateLabels[state] ?? '状态已更新';
}

export function sourceKindLabel(kind?: string | null): string {
  switch (kind) {
    case 'rss': return 'RSS 自动获取';
    case 'search': return '手动搜索';
    case 'manual': return '手动添加';
    default: return '已添加';
  }
}

export function embyItemTypeLabel(itemType?: string | null): string {
  switch (itemType) {
    case 'Series': return '剧集';
    case 'Season': return '季';
    case 'Episode': return '集';
    case 'Movie': return '电影';
    default: return '媒体条目';
  }
}

export function mediaKindLabel(kind?: string | null): string {
  switch (kind) {
    case 'video': return '视频';
    case 'subtitle': return '字幕';
    case 'extra': return '附加内容';
    case 'other': return '其他文件';
    default: return '未识别';
  }
}

// 仅包含 RSS 条目可能持久化的原因：确定性发布分析、Agent 裁决与
// 目标占用硬拒绝。下载文件选择专用原因不应出现在此列表中。
const rssRejectionReasonCodes = [
  'episode_range_batch',
  'non_episode_extra',
  'episode_not_detected',
  'agent_ignored',
  'episode_ambiguous',
  'download_uri_missing',
  'title_include_mismatch',
  'title_excluded',
  'target_episode_in_library',
  'target_episode_imported',
  'target_episode_processing',
] as const;

export type RssRejectionReasonCode = (typeof rssRejectionReasonCodes)[number];

export const rssRejectionReasonOptions: { value: RssRejectionReasonCode; label: string }[] =
  rssRejectionReasonCodes.map((value) => ({ value, label: rejectionLabels[value] ?? value }));

export function reasonLabel(reason?: string | null): string {
  if (!reason) return '';
  return reason
    .split(',')
    .map((part) => rejectionLabels[part.trim()] ?? '不符合自动获取规则')
    .join('、');
}

export function episodeLabel(season?: number | null, episode?: number | null): string {
  if (!season || !episode) return '集数待识别';
  return `第 ${season} 季第 ${episode} 集`;
}
