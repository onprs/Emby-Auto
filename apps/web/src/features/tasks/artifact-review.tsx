import { FileText, Play } from 'lucide-react';
import { useState } from 'react';

import type { ArtifactSet } from '@/api/generated/types.gen';
import { Button } from '@/components/ui/button';
import { formatBytes } from '@/lib/format';

function artifactUrl(taskId: string, artifactId: string): string {
  return `/api/v1/tasks/${encodeURIComponent(taskId)}/artifacts/${encodeURIComponent(artifactId)}/content`;
}

/**
 * Lets the reviewer actually inspect the video and ASS subtitle through the
 * authenticated artifact endpoint. Video uses a native player; when the codec
 * is unsupported the reviewer can still download the file as a fallback.
 */
export function ArtifactReview({ taskId, artifacts }: { taskId: string; artifacts: ArtifactSet }) {
  const [showVideo, setShowVideo] = useState(false);
  const [showSubtitle, setShowSubtitle] = useState(false);
  const [subtitleText, setSubtitleText] = useState<string | null>(null);
  const [videoError, setVideoError] = useState(false);

  const video = artifacts.video;
  const subtitle = artifacts.subtitle;

  const loadSubtitle = async () => {
    setShowSubtitle(true);
    if (subtitleText !== null) {
      return;
    }
    const response = await fetch(artifactUrl(taskId, subtitle.id), { credentials: 'include' });
    setSubtitleText(response.ok ? await response.text() : `无法读取字幕内容（HTTP ${response.status}）`);
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap gap-2">
        <Button type="button" variant="outline" onClick={() => setShowVideo((value) => !value)}>
          <Play />
          {showVideo ? '收起视频' : '播放视频'}
        </Button>
        <Button type="button" variant="outline" onClick={() => void loadSubtitle()}>
          <FileText />
          {showSubtitle ? '刷新字幕' : '查看字幕'}
        </Button>
        <Button asChild variant="ghost">
          <a href={artifactUrl(taskId, video.id)} download>
            下载视频（{formatBytes(video.sizeBytes)}）
          </a>
        </Button>
        <Button asChild variant="ghost">
          <a href={artifactUrl(taskId, subtitle.id)} download>
            下载字幕
          </a>
        </Button>
      </div>

      {showVideo ? (
        <div>
          {videoError ? (
            <div className="border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800" role="alert">
              浏览器无法播放此编码/封装。请使用上方“下载视频”在本地播放器中检查，这不代表文件已损坏。
            </div>
          ) : (
            <video
              className="max-h-[480px] w-full bg-black"
              controls
              preload="metadata"
              src={artifactUrl(taskId, video.id)}
              onError={() => setVideoError(true)}
            >
              您的浏览器不支持视频播放。
            </video>
          )}
        </div>
      ) : null}

      {showSubtitle ? (
        <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words border border-zinc-200 bg-zinc-50 p-4 text-xs text-zinc-800">
          {subtitleText ?? '读取中…'}
        </pre>
      ) : null}
    </div>
  );
}
