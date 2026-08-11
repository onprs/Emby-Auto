import { LoaderCircle } from 'lucide-react';
import { useEffect, useState } from 'react';

import { DetailErrorState, DetailLoadingState, PageBody, PageHeader } from '@/components/resource';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { TranscodeProfileFields } from '@/features/configuration/transcode-profile-fields';
import { SettingsFeedback } from '@/features/configuration/settings-shared';
import { useConfigurationSave, useSettingsData } from '@/features/configuration/use-settings';

export function SettingsTranscodePage() {
  const { configuration, configurationPending, configurationError, refetchConfiguration } = useSettingsData();

  if (configurationPending) {
    return <DetailLoadingState title="转码配置" label="正在读取配置" />;
  }
  if (configurationError || !configuration) {
    return <DetailErrorState title="转码配置" message={configurationError?.message ?? '无法读取配置'} onRetry={() => refetchConfiguration()} />;
  }
  return <TranscodeForm configuration={configuration} />;
}

function TranscodeForm({ configuration }: { configuration: NonNullable<ReturnType<typeof useSettingsData>['configuration']> }) {
  const [transcode, setTranscode] = useState(configuration.transcode);

  useEffect(() => {
    setTranscode(configuration.transcode);
  }, [configuration]);

  const { save, error, conflict, savedAt } = useConfigurationSave(configuration);

  return (
    <PageBody>
      <PageHeader title="转码配置" description="视频编码、质量、音频策略与并发；参数选项会根据编码器与封装格式保持兼容" />
      <SettingsFeedback conflict={conflict} error={error} savedAt={savedAt} />

      <Card>
        <CardHeader>
          <CardTitle>转码参数</CardTitle>
          <CardDescription>选择推荐方案或逐项调整</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-5 sm:grid-cols-2 lg:grid-cols-3">
          <TranscodeProfileFields profile={transcode} onChange={setTranscode} idPrefix="transcode" />
        </CardContent>
      </Card>

      <div className="sticky bottom-0 z-20 mt-8 flex items-center justify-between gap-3 border-t border-zinc-200 bg-surface/95 py-4 backdrop-blur-sm">
        <p className="hidden text-xs text-zinc-500 sm:block">保存对其他分区的配置没有影响</p>
        <Button type="button" variant="accent" disabled={save.isPending} onClick={() => save.mutate({ transcode })}>
          {save.isPending ? <LoaderCircle className="animate-spin" aria-hidden="true" /> : null}
          {save.isPending ? '正在保存' : '保存转码配置'}
        </Button>
      </div>
    </PageBody>
  );
}
