import { LoaderCircle } from "lucide-react";
import { useEffect, useState } from "react";

import {
  DetailErrorState,
  DetailLoadingState,
  PageBody,
  PageHeader,
} from "@/components/resource";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  ConnectivityButton,
  Field,
  SettingsFeedback,
} from "@/features/configuration/settings-shared";
import {
  useConfigurationSave,
  useSettingsData,
} from "@/features/configuration/use-settings";

export function SettingsStoragePage() {
  const {
    configuration,
    configurationPending,
    configurationError,
    refetchConfiguration,
    dependencies,
  } = useSettingsData();

  if (configurationPending) {
    return <DetailLoadingState title="存储与媒体工具" label="正在读取配置" />;
  }
  if (configurationError || !configuration) {
    return (
      <DetailErrorState
        title="存储与媒体工具"
        message={configurationError?.message ?? "无法读取配置"}
        onRetry={() => refetchConfiguration()}
      />
    );
  }
  return (
    <StorageForm configuration={configuration} dependencies={dependencies} />
  );
}

function StorageForm({
  configuration,
  dependencies,
}: {
  configuration: NonNullable<
    ReturnType<typeof useSettingsData>["configuration"]
  >;
  dependencies: ReturnType<typeof useSettingsData>["dependencies"];
}) {
  const [paths, setPaths] = useState(configuration.paths);

  useEffect(() => {
    setPaths(configuration.paths);
  }, [configuration]);

  const { save, error, conflict, savedAt } =
    useConfigurationSave(configuration);

  return (
    <PageBody>
      <PageHeader
        title="存储与媒体工具"
        description="服务器上的目录与 FFmpeg 路径，所有运行环境使用相同挂载路径"
      />
      <SettingsFeedback conflict={conflict} error={error} savedAt={savedAt} />

      <div className="grid gap-5 lg:grid-cols-2">
        <Card className="flex flex-col">
          <CardHeader>
            <CardTitle>存储路径</CardTitle>
            <CardDescription>
              下载缓存、工作目录与最终媒体库根目录
            </CardDescription>
          </CardHeader>
          <CardContent className="grid flex-1 content-start gap-4 sm:grid-cols-2">
            <Field label="下载根目录" htmlFor="downloadRoot">
              <Input
                id="downloadRoot"
                value={paths.downloadRoot}
                onChange={(event) =>
                  setPaths({ ...paths, downloadRoot: event.target.value })
                }
              />
            </Field>
            <Field label="工作根目录" htmlFor="workRoot">
              <Input
                id="workRoot"
                value={paths.workRoot}
                onChange={(event) =>
                  setPaths({ ...paths, workRoot: event.target.value })
                }
              />
            </Field>
            <Field label="暂存根目录" htmlFor="stagingRoot">
              <Input
                id="stagingRoot"
                value={paths.stagingRoot}
                onChange={(event) =>
                  setPaths({ ...paths, stagingRoot: event.target.value })
                }
              />
            </Field>
            <Field label="番剧媒体库目录" htmlFor="animeLibraryRoot">
              <Input
                id="animeLibraryRoot"
                value={paths.animeLibraryRoot}
                onChange={(event) =>
                  setPaths({ ...paths, animeLibraryRoot: event.target.value })
                }
              />
            </Field>
            <Field label="电影媒体库目录" htmlFor="movieLibraryRoot">
              <Input
                id="movieLibraryRoot"
                value={paths.movieLibraryRoot}
                onChange={(event) =>
                  setPaths({ ...paths, movieLibraryRoot: event.target.value })
                }
              />
            </Field>
          </CardContent>
        </Card>

        <Card className="flex flex-col">
          <CardHeader>
            <CardTitle>媒体工具</CardTitle>
            <CardDescription>
              FFmpeg 与 ffprobe 路径（服务器路径）
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-1 flex-col space-y-4">
            <Field label="FFmpeg 路径" htmlFor="ffmpeg">
              <Input
                id="ffmpeg"
                value={paths.ffmpegPath}
                onChange={(event) =>
                  setPaths({ ...paths, ffmpegPath: event.target.value })
                }
              />
            </Field>
            <Field label="ffprobe 路径" htmlFor="ffprobe">
              <Input
                id="ffprobe"
                value={paths.ffprobePath}
                onChange={(event) =>
                  setPaths({ ...paths, ffprobePath: event.target.value })
                }
              />
            </Field>
            <ConnectivityButton
              target="media_tools"
              previous={dependencies?.mediaTools}
              payload={{}}
            />
          </CardContent>
        </Card>
      </div>

      <div className="sticky bottom-0 z-20 mt-8 flex items-center justify-between gap-3 border-t border-zinc-200 bg-surface/95 py-4 backdrop-blur-sm">
        <p className="hidden text-xs text-zinc-500 sm:block">
          保存对其他分区的配置没有影响
        </p>
        <Button
          type="button"
          variant="accent"
          disabled={save.isPending}
          onClick={() => save.mutate({ paths })}
        >
          {save.isPending ? (
            <LoaderCircle className="animate-spin" aria-hidden="true" />
          ) : null}
          {save.isPending ? "正在保存" : "保存存储配置"}
        </Button>
      </div>
    </PageBody>
  );
}
