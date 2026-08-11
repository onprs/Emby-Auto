import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  Check,
  CheckCircle2,
  CircleDashed,
  Eye,
  EyeOff,
  LoaderCircle,
  Unplug,
  XCircle,
} from "lucide-react";
import { useState, type ReactNode } from "react";

import { testConnection } from "@/features/configuration/api";
import type { DashboardDependencyStatus } from "@/api/generated/types.gen";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ErrorState } from "@/components/ui/feedback";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { formatDateTime } from "@/lib/format";
import { cn } from "@/lib/utils";

export function SettingsFeedback({
  conflict,
  error,
  savedAt,
}: {
  conflict: boolean;
  error: string | null;
  savedAt: string | null;
}) {
  return (
    <>
      {conflict ? (
        <div
          className="mb-4 flex flex-wrap items-center justify-between gap-3 rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800"
          role="alert"
        >
          <span>
            配置已被其他操作修改。请刷新页面查看最新配置后再决定是否覆盖。
          </span>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => window.location.reload()}
          >
            刷新
          </Button>
        </div>
      ) : null}
      {error ? <ErrorState className="mb-4" message={error} /> : null}
      {savedAt && !error ? (
        <p
          className="mb-4 flex items-center gap-2 rounded-xl border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-800"
          role="status"
        >
          <CheckCircle2 className="size-4 shrink-0" aria-hidden="true" />
          配置已保存 · {formatDateTime(savedAt)}
        </p>
      ) : null}
    </>
  );
}

export function Field({
  label,
  htmlFor,
  children,
}: {
  label: string;
  htmlFor: string;
  children: ReactNode;
}) {
  return (
    <div className="space-y-2">
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
    </div>
  );
}

const secretIconButtonClass =
  "flex h-10 w-9 shrink-0 cursor-pointer items-center justify-center rounded-lg border border-zinc-300 bg-white text-zinc-500 shadow-sm transition-colors hover:border-zinc-400 hover:text-zinc-800 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-emerald-600 focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50 [&_svg]:size-4";

export function SecretField({
  label,
  value,
  onChange,
  placeholder = "请输入",
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
}) {
  const id = `secret-${label}`;
  const [visible, setVisible] = useState(false);

  return (
    <div className="space-y-2">
      <Label htmlFor={id}>{label}</Label>
      <div className="flex gap-2">
        <Input
          id={id}
          type={visible ? "text" : "password"}
          className="font-mono"
          value={value}
          onChange={(event) => onChange(event.target.value)}
          placeholder={placeholder}
          autoComplete="new-password"
        />
        <button
          type="button"
          aria-label={visible ? `隐藏${label}` : `显示${label}`}
          title={visible ? "隐藏" : "显示"}
          className={secretIconButtonClass}
          onClick={() => setVisible((current) => !current)}
        >
          {visible ? <EyeOff aria-hidden="true" /> : <Eye aria-hidden="true" />}
        </button>
      </div>
    </div>
  );
}

export function ConnectivityButton({
  target,
  payload,
  previous,
  disabled = false,
}: {
  target: "qbittorrent" | "tmdb" | "emby" | "media_tools" | "network_proxy" | "agent";
  payload: object;
  previous?: DashboardDependencyStatus;
  disabled?: boolean;
}) {
  const queryClient = useQueryClient();
  const [result, setResult] = useState<{
    success: boolean;
    code?: string;
    message?: string;
    checkedAt: string;
  } | null>(null);

  const test = useMutation({
    mutationFn: () =>
      testConnection({ target, ...payload } as Parameters<
        typeof testConnection
      >[0]),
    onSuccess: (value) => {
      setResult(value);
      void queryClient.invalidateQueries({ queryKey: ["dashboard"] });
    },
    onError: (cause) =>
      setResult({
        success: false,
        message: cause instanceof Error ? cause.message : "测试失败",
        checkedAt: new Date().toISOString(),
      }),
  });

  const record = previous?.lastTestedAt
    ? {
        success: previous.lastTestSuccess,
        detail: `${previous.lastTestSuccess ? "成功" : `失败${previous.lastTestCode ? ` · ${previous.lastTestCode}` : ""}${previous.lastTestMessage ? ` · ${previous.lastTestMessage}` : ""}`}`,
        testedAt: previous.lastTestedAt,
      }
    : null;

  return (
    <div className="mt-auto space-y-3 border-t border-zinc-100 pt-4">
      <div className="flex flex-wrap items-center gap-3">
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => test.mutate()}
          disabled={test.isPending || disabled}
        >
          {test.isPending ? (
            <LoaderCircle className="animate-spin" aria-hidden="true" />
          ) : (
            <Unplug aria-hidden="true" />
          )}
          {test.isPending ? "正在测试" : "测试连接"}
        </Button>
        {result ? (
          <Badge
            tone={result.success ? "success" : "danger"}
            role="status"
            className="gap-1.5"
          >
            {result.success ? (
              <Check className="size-3.5" aria-hidden="true" />
            ) : (
              <XCircle className="size-3.5" aria-hidden="true" />
            )}
            {result.success ? "连接成功" : "连接失败"}
          </Badge>
        ) : record ? null : (
          <span className="flex items-center gap-1.5 text-xs text-zinc-400">
            <CircleDashed className="size-3.5" aria-hidden="true" />
            尚未测试
          </span>
        )}
      </div>
      {result && !result.success ? (
        <p className="break-words text-xs text-red-600">
          {[result.code, result.message].filter(Boolean).join(" · ") ||
            "未知错误"}
        </p>
      ) : null}
      {result ? (
        <p className="text-xs text-zinc-400">
          本次测试于 {formatDateTime(result.checkedAt)}
        </p>
      ) : record ? (
        <p className="flex items-center gap-1.5 text-xs text-zinc-500">
          {record.success ? (
            <CheckCircle2
              className="size-3.5 shrink-0 text-emerald-600"
              aria-hidden="true"
            />
          ) : (
            <XCircle
              className="size-3.5 shrink-0 text-red-500"
              aria-hidden="true"
            />
          )}
          <span className="min-w-0 break-words">
            上次测试：{record.detail} · {formatDateTime(record.testedAt)}
          </span>
        </p>
      ) : null}
    </div>
  );
}

export function ConfiguredBadge({
  configured,
  configuredLabel,
  missingLabel,
}: {
  configured: boolean;
  configuredLabel: string;
  missingLabel: string;
}) {
  return configured ? (
    <Badge tone="success">{configuredLabel}</Badge>
  ) : (
    <Badge tone="warning">{missingLabel}</Badge>
  );
}
