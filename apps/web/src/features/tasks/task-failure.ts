import type { Acquisition, AcquisitionTaskSummary, OperationSummary, Task } from '@/api/generated/types.gen';
import { downloadWaitsForMapping } from '@/features/downloads/download-presentation';
import { sanitizeTechnicalDetails } from '@/lib/sanitize';

export { sanitizeTechnicalDetails } from '@/lib/sanitize';

export const unknownFailureReason = '未能识别具体失败原因，请查看技术详情或运行日志。';

export type FailureStage = 'download' | 'materialize' | 'video' | 'subtitle' | 'finalize' | 'import' | 'cleanup' | 'unknown';
export type FailureRetryKind = 'download' | 'task' | 'cleanup' | 'none';

export interface TaskFailureInfo {
  stage: FailureStage;
  stageLabel: string;
  summary: string;
  detail: string;
  occurredAt: string;
  attemptLabel: string;
  canRetry: boolean;
  retryKind: FailureRetryKind;
  retryLabel?: '重试下载' | '重试准备处理' | '重试任务' | '重试清理';
  recommendation: string;
  relatedResource: string;
  displayCode?: string;
  latestOperationId?: string;
  latestOperationKind?: string;
  technicalDetails: string;
}

type Reason = {
  reason: string;
  detail: string;
  recommendation: string;
  neverRetry?: boolean;
};

type FailureSource = {
  stage: FailureStage;
  code?: string;
  message?: string;
  occurredAt: string;
  attemptLabel: string;
  baseCanRetry: boolean;
  retryKind: Exclude<FailureRetryKind, 'none'>;
  relatedResource: string;
  latestOperation?: OperationSummary;
};

const stageLabels: Record<FailureStage, string> = {
  download: '下载',
  materialize: '准备媒体处理',
  video: '视频转码',
  subtitle: '字幕处理',
  finalize: '结果校验',
  import: '入库',
  cleanup: '清理',
  unknown: '处理',
};

const operationKinds: Record<FailureStage, string[]> = {
  download: ['download.enqueue', 'download.sync'],
  materialize: ['download.materialize'],
  video: ['transcode.run'],
  subtitle: ['subtitle.prepare'],
  finalize: ['media.finalize'],
  import: ['emby.import', 'emby.refresh'],
  cleanup: ['cleanup.run'],
  unknown: [],
};

const codeReasons: Record<string, Reason> = {
  duplicate_torrent: {
    reason: '下载资源已存在',
    detail: '同一个种子已经存在于下载客户端或当前任务中。',
    recommendation: '打开关联下载确认现有任务，不要重复添加同一资源。',
    neverRetry: true,
  },
  qbittorrent_unavailable: {
    reason: '无法连接 qBittorrent 服务',
    detail: '应用无法登录或访问 qBittorrent，下载状态无法继续同步。',
    recommendation: '检查 qBittorrent 是否运行，并在设置中测试服务连接后重试。',
  },
  qbittorrent_torrent_not_found: {
    reason: 'qBittorrent 中找不到下载任务',
    detail: '应用保存的种子标识已不在 qBittorrent 中。',
    recommendation: '重新下载该资源，不要直接重复当前处理任务。',
    neverRetry: true,
  },
  qbittorrent_enqueue_failed: {
    reason: '无法添加到 qBittorrent',
    detail: 'qBittorrent 没有确认接收这个下载资源。',
    recommendation: '检查下载资源和 qBittorrent 服务状态后重试。',
  },
  qbittorrent_files_unavailable: {
    reason: '无法读取下载文件列表',
    detail: 'qBittorrent 暂时没有返回可供选择的文件。',
    recommendation: '等待元数据完成或检查 qBittorrent 状态后重试。',
  },
  qbittorrent_file_priority_failed: {
    reason: '无法保存下载文件选择',
    detail: 'qBittorrent 没有接受当前的文件优先级设置。',
    recommendation: '检查种子文件列表后重新选择需要下载的文件。',
  },
  qbittorrent_rate_limit_failed: {
    reason: '无法应用下载速率限制',
    detail: '资源已经添加，但 qBittorrent 没有接受当前的上传或下载速率限制。',
    recommendation: '检查 qBittorrent 版本和服务状态后重试；需要不限速时在设置中填写 0。',
  },
  qbittorrent_resume_failed: {
    reason: '下载任务无法启动',
    detail: '资源已经添加，但 qBittorrent 未能恢复下载。',
    recommendation: '检查 qBittorrent 中的任务状态和保存目录后重试。',
  },
  qbittorrent_category_failed: {
    reason: '无法设置下载分类',
    detail: 'qBittorrent 没有接受应用使用的下载分类。',
    recommendation: '检查 qBittorrent 分类和权限设置后重试。',
  },
  qbittorrent_compensation_failed: {
    reason: '下载回滚未完成',
    detail: '下载入队失败后，应用未能完整撤销已添加的 qBittorrent 任务。',
    recommendation: '先检查 qBittorrent 中是否残留同一种子，再重试下载。',
  },
  download_storage_unavailable: {
    reason: '下载目录不可用',
    detail: '应用无法读取或写入下载状态和下载目录。',
    recommendation: '检查下载目录、数据库和磁盘状态后重试。',
  },
  download_source_not_downloadable: {
    reason: '下载地址不可用',
    detail: '当前内容没有可提交给下载客户端的有效种子或下载地址。',
    recommendation: '重新搜索或更换下载资源。',
    neverRetry: true,
  },
  download_hash_missing: {
    reason: '下载任务标识缺失',
    detail: '应用无法确认该记录对应的 qBittorrent 任务。',
    recommendation: '打开 qBittorrent 核对任务；无法恢复时重新下载该资源。',
    neverRetry: true,
  },
  download_file_selection_invalid: {
    reason: '下载文件选择无效',
    detail: '选中的文件无法组成可处理的视频和中文字幕。',
    recommendation: '重新选择正片和字幕文件，或更换下载资源。',
    neverRetry: true,
  },
  episode_mapping_required: {
    reason: '需要确认剧集对应关系',
    detail: '下载文件尚未完整对应到目标季和集。',
    recommendation: '先完成剧集对应关系设置，再继续处理。',
    neverRetry: true,
  },
  download_no_selected_video: {
    reason: '没有可处理的正片视频',
    detail: '下载文件中没有选中可作为正片的视频。',
    recommendation: '重新选择文件或更换包含正片视频的下载资源。',
    neverRetry: true,
  },
  download_no_main_video: {
    reason: '没有可处理的正片视频',
    detail: '下载资源中没有识别出可处理的正片视频。',
    recommendation: '可点击“重试下载”重新解析文件；若仍未识别，请更换包含正片视频的下载资源或在文件选择中明确指定正片。',
  },
  no_main_video: {
    reason: '没有可处理的正片视频',
    detail: '下载资源中没有识别出可处理的正片视频。',
    recommendation: '更换下载资源，或在文件选择中明确指定正片。',
    neverRetry: true,
  },
  media_storage_unavailable: {
    reason: '无法保存处理进度',
    detail: '媒体文件处理状态暂时无法写入数据库或暂存区。',
    recommendation: '检查数据库、暂存目录和磁盘状态后重试。',
  },
  source_video_path_invalid: {
    reason: '源视频路径不安全',
    detail: '源视频不在允许处理的下载目录中。',
    recommendation: '检查下载根目录和文件归属，不要直接重试当前任务。',
    neverRetry: true,
  },
  source_video_probe_failed: {
    reason: '无法读取源视频',
    detail: 'FFprobe 无法读取关联源视频的媒体信息。',
    recommendation: '确认源文件完整且可正常播放后再重试。',
  },
  ffmpeg_transcode_failed: {
    reason: 'FFmpeg 未能完成视频转换',
    detail: 'FFmpeg 在转换源视频时异常退出，未生成可用视频。',
    recommendation: '查看技术详情，检查源文件和转码配置后重试。',
  },
  transcode_output_invalid: {
    reason: '转码结果未通过校验',
    detail: '生成的视频不符合当前封装、编码或像素格式要求。',
    recommendation: '检查转码配置和源视频兼容性后重试。',
  },
  transcode_output_probe_failed: {
    reason: '无法校验转码结果',
    detail: 'FFprobe 无法读取刚生成的视频文件。',
    recommendation: '检查 FFmpeg/FFprobe 配置和磁盘状态后重试。',
  },
  transcode_output_commit_failed: {
    reason: '无法保存转码结果',
    detail: '生成的视频未能安全写入暂存目录。',
    recommendation: '检查暂存目录权限和磁盘空间后重试。',
  },
  transcode_output_path_invalid: {
    reason: '转码输出路径无效',
    detail: '计划写入的视频位置不在允许的暂存目录中。',
    recommendation: '检查暂存目录配置和任务文件归属，不要直接重试。',
    neverRetry: true,
  },
  transcode_output_conflict: {
    reason: '暂存区存在冲突视频',
    detail: '目标位置已有与当前任务不匹配的视频文件。',
    recommendation: '核对并处理暂存区中的冲突文件后重新执行任务。',
    neverRetry: true,
  },
  transcode_output_unavailable: {
    reason: '转码结果无法读取',
    detail: '视频生成后无法从暂存目录重新读取。',
    recommendation: '检查暂存目录、磁盘和文件占用后重试。',
  },
  simplified_chinese_subtitle_not_found: {
    reason: '没有找到简体中文字幕',
    detail: '下载内容中没有可确认的简体中文字幕。',
    recommendation: '补充简体中文字幕或更换字幕完整的下载资源。',
    neverRetry: true,
  },
  subtitle_format_unsupported: {
    reason: '中文字幕格式不受支持',
    detail: '选中的字幕无法转换为独立 ASS 字幕。',
    recommendation: '更换 SRT、ASS 等受支持的文本字幕。',
    neverRetry: true,
  },
  ffmpeg_subtitle_failed: {
    reason: 'FFmpeg 未能准备字幕',
    detail: '内置字幕提取或外置字幕转换没有成功。',
    recommendation: '检查字幕文件是否完整，并查看技术详情后重试。',
  },
  subtitle_copy_failed: {
    reason: '无法复制字幕文件',
    detail: '字幕文件未能写入暂存目录。',
    recommendation: '检查字幕文件、暂存目录权限和磁盘空间后重试。',
  },
  subtitle_output_invalid: {
    reason: '字幕结果未通过校验',
    detail: '生成的 ASS 字幕内容不完整或格式无效。',
    recommendation: '更换字幕来源或检查字幕转换配置后重试。',
  },
  subtitle_selection_failed: {
    reason: '无法选择中文字幕',
    detail: '下载内容中的字幕来源无法形成可处理的简体中文字幕。',
    recommendation: '重新选择字幕文件或更换字幕完整的下载资源。',
    neverRetry: true,
  },
  subtitle_plan_invalid: {
    reason: '字幕转换方案无效',
    detail: '当前字幕来源无法生成有效的 FFmpeg 转换命令。',
    recommendation: '更换字幕来源或检查媒体工具配置。',
    neverRetry: true,
  },
  subtitle_output_commit_failed: {
    reason: '无法保存字幕结果',
    detail: '生成的 ASS 字幕未能安全写入暂存目录。',
    recommendation: '检查暂存目录权限和磁盘空间后重试。',
  },
  subtitle_output_path_invalid: {
    reason: '字幕输出路径无效',
    detail: '计划写入的字幕位置不在允许的暂存目录中。',
    recommendation: '检查暂存目录配置和任务文件归属，不要直接重试。',
    neverRetry: true,
  },
  external_subtitle_path_invalid: {
    reason: '外置字幕路径不安全',
    detail: '选中的外置字幕不在允许读取的下载目录中。',
    recommendation: '重新选择下载目录内的字幕文件。',
    neverRetry: true,
  },
  subtitle_output_conflict: {
    reason: '暂存区存在冲突字幕',
    detail: '目标位置已有与当前任务不匹配的 ASS 字幕。',
    recommendation: '核对并处理暂存区中的冲突文件后重新执行任务。',
    neverRetry: true,
  },
  subtitle_output_unavailable: {
    reason: '字幕结果无法读取',
    detail: 'ASS 字幕生成后无法从暂存目录重新读取。',
    recommendation: '检查暂存目录、磁盘和文件占用后重试。',
  },
  artifact_basename_mismatch: {
    reason: '视频与字幕文件名不一致',
    detail: '视频和字幕没有形成可供审核、入库的配对文件。',
    recommendation: '重新执行视频和字幕处理；仍失败时检查命名配置。',
  },
  video_artifact_invalid: {
    reason: '视频文件校验失败',
    detail: '处理后的视频文件缺失或校验值不一致。',
    recommendation: '重新处理视频；再次失败时重新下载源文件。',
  },
  subtitle_artifact_invalid: {
    reason: '字幕文件校验失败',
    detail: '处理后的 ASS 字幕缺失或校验值不一致。',
    recommendation: '重新处理字幕；再次失败时更换字幕来源。',
  },
  library_import_failed: {
    reason: '无法写入媒体库',
    detail: '配对的视频和字幕没有完整写入媒体库目录。',
    recommendation: '检查媒体库目录、磁盘空间和权限后重试入库。',
  },
  library_path_invalid: {
    reason: '媒体库路径不安全',
    detail: '计划写入的位置不在已配置的 Emby 媒体库目录中。',
    recommendation: '检查媒体库根目录和命名配置，不要直接重试当前任务。',
    neverRetry: true,
  },
  library_destination_conflict: {
    reason: '媒体库中存在冲突文件',
    detail: '目标位置已经存在内容不同的同名文件。',
    recommendation: '检查媒体库中的同名文件，确认保留内容后再重试。',
    neverRetry: true,
  },
  emby_refresh_failed: {
    reason: '无法连接 Emby 服务',
    detail: '文件已处理，但应用无法请求 Emby 刷新媒体库。',
    recommendation: '检查 Emby 服务连接，通过设置页连接测试后重试。',
  },
  emby_authentication_failed: {
    reason: 'Emby 身份验证失败',
    detail: 'Emby 拒绝了当前 API 凭据。',
    recommendation: '在设置中更新 Emby API Key 并通过连接测试。',
    neverRetry: true,
  },
  emby_catalog_not_found: {
    reason: 'Emby 未识别媒体文件',
    detail: 'Emby 刷新后仍没有找到对应媒体条目。',
    recommendation: '检查文件命名、媒体库目录和 Emby 扫描设置后再次扫描。',
    neverRetry: true,
  },
  cleanup_delete_failed: {
    reason: '无法删除临时文件',
    detail: '清理程序未能删除下载源文件或转码暂存文件。',
    recommendation: '检查文件占用、目录权限和磁盘状态后重试清理。',
  },
  cleanup_staged_files_remain: {
    reason: '仍有临时文件未删除',
    detail: '清理结束后仍检测到暂存视频或字幕。',
    recommendation: '确认文件未被占用并检查目录权限后重试清理。',
  },
  qbittorrent_cleanup_failed: {
    reason: '无法清理 qBittorrent 任务',
    detail: '应用没有成功移除对应的 qBittorrent 任务。',
    recommendation: '检查 qBittorrent 服务连接后重试清理。',
  },
  cleanup_download_identity_missing: {
    reason: '缺少下载清理标识',
    detail: '应用无法安全确认需要移除的种子任务和下载目录。',
    recommendation: '核对关联下载和 qBittorrent 任务，不要直接重复清理。',
    neverRetry: true,
  },
  cleanup_path_unsafe: {
    reason: '安全检查阻止删除文件',
    detail: '目标路径不在允许清理的下载、工作或暂存目录中。',
    recommendation: '检查路径配置和文件归属，不要反复重试当前清理。',
    neverRetry: true,
  },
};

const configurationCodes = new Set([
  'configuration_unavailable',
  'media_configuration_unavailable',
  'media_configuration_invalid',
  'transcode_profile_invalid',
  'transcode_profile_required',
  'download_root_not_configured',
  'cleanup_roots_not_configured',
  'staging_unavailable',
  'anime_library_root_not_configured',
  'movie_library_root_not_configured',
  'emby_configuration_invalid',
  'emby_not_configured',
  'qbittorrent_configuration_invalid',
  'qbittorrent_not_configured',
]);

function configurationReason(stage: FailureStage): Reason {
  const mediaStage = stage === 'video' || stage === 'subtitle' || stage === 'finalize';
  return {
    reason: mediaStage ? '媒体处理配置不可用' : `${stageLabels[stage]}配置不可用`,
    detail: '当前运行配置缺失、无效或暂时无法读取。',
    recommendation: '前往设置检查相关路径、服务和转码配置，通过连接测试后重新处理。',
    neverRetry: true,
  };
}

function patternReason(stage: FailureStage, message: string): Reason | undefined {
  const text = message.toLowerCase();
  if (/no space left|disk (?:is )?full|not enough space|insufficient (?:disk )?space|磁盘空间不足/.test(text)) {
    return {
      reason: '磁盘空间不足',
      detail: '目标磁盘没有足够空间继续写入文件。',
      recommendation: '释放下载、暂存或媒体库所在磁盘的空间后重试。',
    };
  }
  if (/used by another process|resource busy|sharing violation|file is in use|文件.*占用/.test(text)) {
    return {
      reason: '文件正在被其他进程占用',
      detail: '系统拒绝移动或删除仍被其他程序打开的文件。',
      recommendation: '关闭占用文件的程序，确认文件不再被扫描或播放后重试清理。',
    };
  }
  if (/permission denied|access is denied|operation not permitted|not permitted|拒绝访问|权限/.test(text)) {
    return {
      reason: stage === 'cleanup' ? '没有文件删除权限' : '没有文件读写权限',
      detail: '运行账户没有访问目标文件或目录所需的权限。',
      recommendation: '检查目录权限和运行账户权限，修正后再重试。',
    };
  }
  if (/no such file|cannot find the file|file not found|path not found|找不到.*文件/.test(text)) {
    return {
      reason: stage === 'download' ? '下载文件不存在' : '源文件不存在',
      detail: '任务引用的源文件已经移动、删除或从未完整下载。',
      recommendation: '重新下载或重新选择源文件，不要直接重复当前处理。',
      neverRetry: true,
    };
  }
  if (/invalid data found|unsupported (?:codec|format)|could not find codec parameters|invalid media|unknown format/.test(text)) {
    return {
      reason: '源文件格式不受支持',
      detail: '媒体工具无法解析源视频，文件可能损坏或使用了当前配置不支持的格式。',
      recommendation: '重新下载或更换可正常播放的源文件后重新处理。',
      neverRetry: true,
    };
  }
  if (stage === 'download' && /(?:http\s*)?404|not found.*torrent|torrent.*(?:expired|invalid)|invalid torrent|invalid metainfo/.test(text)) {
    return {
      reason: '种子文件已失效',
      detail: '下载地址已失效，或返回的内容不再是有效种子文件。',
      recommendation: '更换下载资源或重新搜索有效种子。',
      neverRetry: true,
    };
  }
  if (stage === 'import' && /connection refused|connect.*timed out|no connection could be made|emby.*unavailable/.test(text)) {
    return codeReasons.emby_refresh_failed;
  }
  return undefined;
}

function resolveReason(stage: FailureStage, code?: string, message?: string): { value: Reason; recognized: boolean } {
  const pattern = patternReason(stage, message ?? '');
  if (pattern) {
    return { value: pattern, recognized: true };
  }
  if (code && configurationCodes.has(code)) {
    return { value: configurationReason(stage), recognized: true };
  }
  if (code && codeReasons[code]) {
    return { value: codeReasons[code], recognized: true };
  }
  return {
    value: { reason: unknownFailureReason, detail: unknownFailureReason, recommendation: '展开技术详情或打开运行记录，确认原因后再决定是否重试。' },
    recognized: false,
  };
}

function stageFromTask(stage?: Task['failureStage']): FailureStage {
  return stage ?? 'unknown';
}

function latestOperation(operations: OperationSummary[], stage: FailureStage): OperationSummary | undefined {
  const kinds = operationKinds[stage];
  return operations
    .filter((operation) => kinds.includes(operation.kind))
    .sort((left, right) => Date.parse(right.updatedAt) - Date.parse(left.updatedAt))[0];
}

function operationAttemptLabel(operations: OperationSummary[], stage: FailureStage, latest?: OperationSummary): string {
  const runCount = Math.max(1, operations.filter((operation) => operationKinds[stage].includes(operation.kind)).length);
  const base = `第 ${runCount} 次执行`;
  if (!latest || latest.maxAttempts <= 0) {
    return base;
  }
  return `${base} · 最近一次尝试 ${latest.attemptCount}/${latest.maxAttempts}`;
}

function relatedTaskResource(task: Task, stage: FailureStage): string {
  const title = task.mediaType === 'movie' ? (task.movieTitle ?? '当前电影') : (task.seriesTitle ?? '当前剧集');
  switch (stage) {
    case 'video': return `《${title}》的源视频`;
    case 'subtitle': return `《${title}》的中文字幕`;
    case 'finalize': return `《${title}》的处理后视频与 ASS 字幕`;
    case 'import': return `Emby 服务及《${title}》的待入库文件`;
    case 'cleanup': return '关联下载、种子任务和转码临时文件';
    default: return `《${title}》的关联资源`;
  }
}

function buildFailureInfo(source: FailureSource): TaskFailureInfo {
  const resolved = resolveReason(source.stage, source.code, source.message);
  const canRetry = source.baseCanRetry && !resolved.value.neverRetry;
  const retryKind = canRetry ? source.retryKind : 'none';
  const retryLabel = retryKind === 'download'
    ? (source.stage === 'materialize' ? '重试准备处理' : '重试下载')
    : retryKind === 'cleanup'
      ? '重试清理'
      : retryKind === 'task'
        ? '重试任务'
        : undefined;
  const technical = technicalText(source.code, source.message, source.latestOperation);
  return {
    stage: source.stage,
    stageLabel: stageLabels[source.stage],
    summary: `${stageLabels[source.stage]}失败：${resolved.value.reason}`,
    detail: resolved.value.detail,
    occurredAt: source.occurredAt,
    attemptLabel: source.attemptLabel,
    canRetry,
    retryKind,
    retryLabel,
    recommendation: resolved.value.recommendation,
    relatedResource: source.relatedResource,
    displayCode: resolved.recognized && source.code ? source.code : undefined,
    latestOperationId: source.latestOperation?.id,
    latestOperationKind: source.latestOperation?.kind,
    technicalDetails: technical,
  };
}

export function taskFailureInfo(task: Task): TaskFailureInfo | null {
  if (task.cleanup?.status === 'failed') {
    const stage: FailureStage = 'cleanup';
    const operation = latestOperation(task.operations, stage);
    return buildFailureInfo({
      stage,
      code: task.cleanup.errorCode ?? operation?.errorCode,
      message: task.cleanup.errorMessage ?? operation?.errorMessage,
      occurredAt: task.cleanup.updatedAt,
      attemptLabel: operation
        ? `第 ${task.cleanup.attempt} 次清理 · 最近一次尝试 ${operation.attemptCount}/${operation.maxAttempts}`
        : `第 ${task.cleanup.attempt} 次清理`,
      baseCanRetry: true,
      retryKind: 'cleanup',
      relatedResource: relatedTaskResource(task, stage),
      latestOperation: operation,
    });
  }
  const isFailed = task.state === 'failed';
  const isProcessingStuck = task.state === 'processing' && (task.videoState === 'failed' || task.subtitleState === 'failed');
  if (!isFailed && !isProcessingStuck) {
    return null;
  }
  const videoFailed = task.videoState === 'failed';
  const subtitleFailed = task.subtitleState === 'failed';
  let stage: FailureStage;
  if (isFailed && task.failureStage) {
    stage = stageFromTask(task.failureStage);
  } else if (videoFailed || subtitleFailed) {
    if (videoFailed && subtitleFailed) {
      const fromStage = stageFromTask(task.failureStage);
      if (fromStage === 'video' || fromStage === 'subtitle') {
        stage = fromStage;
      } else {
        stage = 'video';
      }
    } else if (videoFailed) {
      stage = 'video';
    } else {
      stage = 'subtitle';
    }
  } else {
    stage = stageFromTask(task.failureStage);
  }
  const operation = latestOperation(task.operations, stage);
  const nestedImport = stage === 'import' && task.import?.status === 'failed' ? task.import : undefined;
  return buildFailureInfo({
    stage,
    code: nestedImport?.errorCode ?? task.errorCode ?? operation?.errorCode,
    message: nestedImport?.errorMessage ?? task.errorMessage ?? operation?.errorMessage,
    occurredAt: nestedImport?.updatedAt ?? operation?.finishedAt ?? operation?.updatedAt ?? task.updatedAt,
    attemptLabel: nestedImport
      ? `${operationAttemptLabel(task.operations, stage, operation)} · 第 ${nestedImport.attempt} 次入库`
      : operationAttemptLabel(task.operations, stage, operation),
    baseCanRetry: task.actions.canRetry,
    retryKind: 'task',
    relatedResource: relatedTaskResource(task, stage),
    latestOperation: operation,
  });
}

function acquisitionTaskResource(item: Acquisition, task: AcquisitionTaskSummary, stage: FailureStage): string {
  const title = item.mediaType === 'movie' ? (item.movieTitle ?? '当前电影') : (item.seriesTitle ?? '当前剧集');
  if (stage === 'import') return `Emby 服务及《${title}》的待入库文件`;
  if (stage === 'subtitle') return `《${title}》的中文字幕`;
  return `《${title}》的源视频`;
}

export function acquisitionFailureInfo(item: Acquisition): TaskFailureInfo | null {
  if (item.download?.status === 'failed') {
    if (downloadWaitsForMapping(item.download)) return null;
    const stage: FailureStage = item.download.failureStage === 'materialize' ? 'materialize' : 'download';
    const title = item.mediaType === 'movie' ? (item.movieTitle ?? '当前电影') : (item.seriesTitle ?? '当前剧集');
    return buildFailureInfo({
      stage,
      code: item.download.errorCode,
      message: item.download.errorMessage,
      occurredAt: item.download.updatedAt,
      attemptLabel: stage === 'materialize' ? `第 ${item.download.attempt} 次下载后的处理` : `第 ${item.download.attempt} 次下载`,
      baseCanRetry: Boolean(item.download.failureStage),
      retryKind: 'download',
      relatedResource: stage === 'materialize' ? `《${title}》的已下载文件和媒体处理配置` : `${title}的种子文件和下载目录`,
    });
  }
  const failedTask = item.tasks.find((task) => task.state === 'failed');
  if (!failedTask) {
    return null;
  }
  const stage = stageFromTask(failedTask.failureStage);
  return buildFailureInfo({
    stage,
    code: failedTask.errorCode,
    message: failedTask.errorMessage,
    occurredAt: failedTask.updatedAt,
    attemptLabel: '第 1 次执行',
    baseCanRetry: Boolean(failedTask.failureStage),
    retryKind: 'task',
    relatedResource: acquisitionTaskResource(item, failedTask, stage),
  });
}

function technicalText(code?: string, message?: string, operation?: OperationSummary): string {
  const lines: string[] = [];
  if (code) lines.push(`错误标识：${code}`);
  if (message) lines.push(`后端信息：${message}`);
  if (operation?.errorCode && operation.errorCode !== code) lines.push(`运行错误标识：${operation.errorCode}`);
  if (operation?.errorMessage && operation.errorMessage !== message) lines.push(`运行信息：${operation.errorMessage}`);
  if (lines.length === 0) return '后端没有记录额外技术信息。';
  return sanitizeTechnicalDetails(lines.join('\n'));
}
