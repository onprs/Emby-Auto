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
  ConfiguredBadge,
  ConnectivityButton,
  Field,
  SecretField,
  SettingsFeedback,
} from "@/features/configuration/settings-shared";
import { revealSecrets } from "@/features/configuration/api";
import {
  secretPayload,
  useConfigurationSave,
  useSettingsData,
} from "@/features/configuration/use-settings";

function nonnegativeInteger(value: number): number {
  if (!Number.isFinite(value)) return 0;
  return Math.min(2147483647, Math.max(0, Math.trunc(value)));
}

export function SettingsServicesPage() {
  const {
    configuration,
    configurationPending,
    configurationError,
    refetchConfiguration,
    dependencies,
  } = useSettingsData();

  if (configurationPending) {
    return <DetailLoadingState title="外部服务" label="正在读取配置" />;
  }
  if (configurationError || !configuration) {
    return (
      <DetailErrorState
        title="外部服务"
        message={configurationError?.message ?? "无法读取配置"}
        onRetry={() => refetchConfiguration()}
      />
    );
  }
  return (
    <ServicesForm configuration={configuration} dependencies={dependencies} />
  );
}

function ServicesForm({
  configuration,
  dependencies,
}: {
  configuration: NonNullable<
    ReturnType<typeof useSettingsData>["configuration"]
  >;
  dependencies: ReturnType<typeof useSettingsData>["dependencies"];
}) {
  const [qbUrl, setQbUrl] = useState(configuration.qBittorrent.url);
  const [qbUsername, setQbUsername] = useState(
    configuration.qBittorrent.username,
  );
  const [qbDownloadRateLimit, setQbDownloadRateLimit] = useState(
    configuration.qBittorrent.downloadRateLimitKibPerSecond,
  );
  const [qbUploadRateLimit, setQbUploadRateLimit] = useState(
    configuration.qBittorrent.uploadRateLimitKibPerSecond,
  );
  const [qbPassword, setQbPassword] = useState("");
  const [embyUrl, setEmbyUrl] = useState(configuration.emby.url);
  const [embyKey, setEmbyKey] = useState("");
  const [tmdbToken, setTmdbToken] = useState("");
  const [networkProxy, setNetworkProxy] = useState(configuration.networkProxy);
  const [eventsRetentionDays, setEventsRetentionDays] = useState(
    configuration.events.retentionDays,
  );
  const [savedSecrets, setSavedSecrets] = useState<{
    qbPassword: string | null;
    embyApiKey: string | null;
    tmdbApiToken: string | null;
  }>({ qbPassword: null, embyApiKey: null, tmdbApiToken: null });

  useEffect(() => {
    setQbUrl(configuration.qBittorrent.url);
    setQbUsername(configuration.qBittorrent.username);
    setQbDownloadRateLimit(
      configuration.qBittorrent.downloadRateLimitKibPerSecond,
    );
    setQbUploadRateLimit(configuration.qBittorrent.uploadRateLimitKibPerSecond);
    setEmbyUrl(configuration.emby.url);
    setNetworkProxy(configuration.networkProxy);
    setEventsRetentionDays(configuration.events.retentionDays);

    if (
      configuration.qBittorrent.password.configured ||
      configuration.emby.apiKey.configured ||
      configuration.tmdb.apiToken.configured
    ) {
      let cancelled = false;
      revealSecrets()
        .then((secrets) => {
          if (cancelled) return;
          setQbPassword(secrets.qbPassword ?? "");
          setEmbyKey(secrets.embyApiKey ?? "");
          setTmdbToken(secrets.tmdbApiToken ?? "");
          setSavedSecrets({
            qbPassword: secrets.qbPassword ?? null,
            embyApiKey: secrets.embyApiKey ?? null,
            tmdbApiToken: secrets.tmdbApiToken ?? null,
          });
        })
        .catch(() => {
          /* 拉取失败时保持空值，仍可输入新值保存 */
        });
      return () => {
        cancelled = true;
      };
    }
    setQbPassword("");
    setEmbyKey("");
    setTmdbToken("");
    setSavedSecrets({ qbPassword: null, embyApiKey: null, tmdbApiToken: null });
    return undefined;
  }, [configuration]);

  const { save, error, conflict, savedAt } =
    useConfigurationSave(configuration);

  const qbPasswordPayload = () =>
    secretPayload(
      qbPassword,
      savedSecrets.qbPassword,
      configuration.qBittorrent.password.configured,
    );
  const embyKeyPayload = () =>
    secretPayload(
      embyKey,
      savedSecrets.embyApiKey,
      configuration.emby.apiKey.configured,
    );
  const tmdbTokenPayload = () =>
    secretPayload(
      tmdbToken,
      savedSecrets.tmdbApiToken,
      configuration.tmdb.apiToken.configured,
    );

  return (
    <PageBody>
      <PageHeader
        title="外部服务"
        description="下载客户端、媒体服务器与元数据源；修改后建议先测试连接再保存"
      />
      <SettingsFeedback conflict={conflict} error={error} savedAt={savedAt} />

      <div className="grid gap-5 lg:grid-cols-2">
        <Card className="flex flex-col">
          <CardHeader
            fill
            action={
              <ConfiguredBadge
                configured={configuration.qBittorrent.password.configured}
                configuredLabel="密码已配置"
                missingLabel="密码未配置"
              />
            }
          >
            <CardTitle>qBittorrent</CardTitle>
            <CardDescription>
              Bt 下载客户端，负责 RSS 与搜索资源的下载
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-1 flex-col space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="URL" htmlFor="qb-url">
                <Input
                  id="qb-url"
                  value={qbUrl}
                  onChange={(event) => setQbUrl(event.target.value)}
                />
              </Field>
              <Field label="用户名" htmlFor="qb-username">
                <Input
                  id="qb-username"
                  value={qbUsername}
                  onChange={(event) => setQbUsername(event.target.value)}
                />
              </Field>
            </div>
            <SecretField
              label="密码"
              value={qbPassword}
              onChange={setQbPassword}
            />
            <div className="grid gap-4 sm:grid-cols-2">
              <Field
                label="下载速率限制（KiB/s）"
                htmlFor="qb-download-rate-limit"
              >
                <Input
                  id="qb-download-rate-limit"
                  type="number"
                  min={0}
                  max={2147483647}
                  step={1}
                  value={qbDownloadRateLimit}
                  onChange={(event) =>
                    setQbDownloadRateLimit(
                      nonnegativeInteger(event.currentTarget.valueAsNumber),
                    )
                  }
                />
              </Field>
              <Field
                label="上传速率限制（KiB/s）"
                htmlFor="qb-upload-rate-limit"
              >
                <Input
                  id="qb-upload-rate-limit"
                  type="number"
                  min={0}
                  max={2147483647}
                  step={1}
                  value={qbUploadRateLimit}
                  onChange={(event) =>
                    setQbUploadRateLimit(
                      nonnegativeInteger(event.currentTarget.valueAsNumber),
                    )
                  }
                />
              </Field>
            </div>
            <p className="text-xs text-zinc-500">
              设置为 0 时不限速，仅应用于 Emby Auto 管理的下载。
            </p>
            <ConnectivityButton
              target="qbittorrent"
              previous={dependencies?.qBittorrent}
              payload={{
                qBittorrent: {
                  url: qbUrl,
                  username: qbUsername,
                  password: qbPasswordPayload(),
                  downloadRateLimitKibPerSecond: qbDownloadRateLimit,
                  uploadRateLimitKibPerSecond: qbUploadRateLimit,
                },
              }}
            />
          </CardContent>
        </Card>

        <Card className="flex flex-col">
          <CardHeader
            fill
            action={
              <ConfiguredBadge
                configured={configuration.emby.apiKey.configured}
                configuredLabel="API key 已配置"
                missingLabel="API key 未配置"
              />
            }
          >
            <CardTitle>Emby</CardTitle>
            <CardDescription>
              媒体服务器，入库完成后自动刷新媒体库
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-1 flex-col space-y-4">
            <Field label="URL" htmlFor="emby-url">
              <Input
                id="emby-url"
                value={embyUrl}
                onChange={(event) => setEmbyUrl(event.target.value)}
              />
            </Field>
            <SecretField
              label="API key"
              value={embyKey}
              onChange={setEmbyKey}
            />
            <ConnectivityButton
              target="emby"
              previous={dependencies?.emby}
              payload={{
                emby: { url: embyUrl, apiKey: embyKeyPayload() },
              }}
            />
          </CardContent>
        </Card>

        <Card className="flex flex-col">
          <CardHeader
            fill
            action={
              <ConfiguredBadge
                configured={configuration.tmdb.apiToken.configured}
                configuredLabel="Token 已配置"
                missingLabel="Token 未配置"
              />
            }
          >
            <CardTitle>TMDb</CardTitle>
            <CardDescription>
              元数据来源，用于搜索作品与剧集映射
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-1 flex-col space-y-4">
            <SecretField
              label="API Read Access Token"
              value={tmdbToken}
              onChange={setTmdbToken}
            />
            <ConnectivityButton
              target="tmdb"
              previous={dependencies?.tmdb}
              payload={{ tmdb: { apiToken: tmdbTokenPayload() } }}
            />
          </CardContent>
        </Card>

        <Card className="flex flex-col">
          <CardHeader
            fill
            action={
              networkProxy.enabled ? (
                <ConfiguredBadge
                  configured
                  configuredLabel="已启用"
                  missingLabel="未启用"
                />
              ) : (
                <ConfiguredBadge
                  configured={false}
                  configuredLabel="已启用"
                  missingLabel="未启用"
                />
              )
            }
          >
            <CardTitle>网络代理</CardTitle>
            <CardDescription>访问外部服务时使用的 HTTP 代理</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-1 flex-col space-y-4">
            <label
              className="flex items-center gap-3 text-sm font-medium"
              htmlFor="network-proxy-enabled"
            >
              <input
                id="network-proxy-enabled"
                type="checkbox"
                className="size-4 rounded border-zinc-300 accent-emerald-600"
                checked={networkProxy.enabled}
                onChange={(event) =>
                  setNetworkProxy({
                    ...networkProxy,
                    enabled: event.target.checked,
                  })
                }
              />
              启用代理
            </label>
            <Field label="代理 URL" htmlFor="network-proxy-url">
              <Input
                id="network-proxy-url"
                type="url"
                placeholder="http://127.0.0.1:7890"
                value={networkProxy.url}
                disabled={!networkProxy.enabled}
                onChange={(event) =>
                  setNetworkProxy({ ...networkProxy, url: event.target.value })
                }
              />
            </Field>
            <ConnectivityButton
              target="network_proxy"
              previous={dependencies?.networkProxy}
              payload={{ networkProxy }}
              disabled={!networkProxy.enabled || networkProxy.url.trim() === ""}
            />
          </CardContent>
        </Card>

        <Card className="flex flex-col">
          <CardHeader fill>
            <CardTitle>事件历史</CardTitle>
            <CardDescription>
              系统事件记录保留策略，由后台定期任务清理过期事件
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-1 flex-col space-y-4">
            <Field label="保留天数" htmlFor="events-retention-days">
              <Input
                id="events-retention-days"
                type="number"
                min={0}
                max={36500}
                step={1}
                value={eventsRetentionDays}
                onChange={(event) =>
                  setEventsRetentionDays(
                    nonnegativeInteger(event.currentTarget.valueAsNumber),
                  )
                }
              />
            </Field>
            <p className="text-xs text-zinc-500">
              超过保留天数的事件会被定期删除；设置为 0 时保留全部事件历史。
            </p>
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
          onClick={() =>
            save.mutate({
              qBittorrent: {
                url: qbUrl,
                username: qbUsername,
                password: qbPasswordPayload(),
                downloadRateLimitKibPerSecond: qbDownloadRateLimit,
                uploadRateLimitKibPerSecond: qbUploadRateLimit,
              },
              emby: { url: embyUrl, apiKey: embyKeyPayload() },
              tmdb: { apiToken: tmdbTokenPayload() },
              networkProxy,
              events: { retentionDays: eventsRetentionDays },
            })
          }
        >
          {save.isPending ? (
            <LoaderCircle className="animate-spin" aria-hidden="true" />
          ) : null}
          {save.isPending ? "正在保存" : "保存外部服务配置"}
        </Button>
      </div>
    </PageBody>
  );
}
