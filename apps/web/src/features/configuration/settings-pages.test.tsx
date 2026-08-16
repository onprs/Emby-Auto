import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";

import type {
  Configuration,
  UpdateConfigurationRequest,
} from "@/api/generated/types.gen";
import { SettingsAgentPage } from "@/features/configuration/settings-agent-page";
import { SettingsHubPage } from "@/features/configuration/settings-hub-page";
import { SettingsServicesPage } from "@/features/configuration/settings-services-page";
import { SettingsStoragePage } from "@/features/configuration/settings-storage-page";
import { SettingsTranscodePage } from "@/features/configuration/settings-transcode-page";
import { server } from "@/test/msw-server";
import { renderWithProviders } from "@/test/render";

const baseConfiguration: Configuration = {
  version: 3,
  qBittorrent: {
    url: "http://qb:8080",
    username: "admin",
    password: { configured: true, masked: "••••ab" },
    downloadRateLimitKibPerSecond: 2048,
    uploadRateLimitKibPerSecond: 512,
  },
  emby: {
    url: "http://emby:8096",
    apiKey: { configured: true, masked: "••••cd" },
  },
  tmdb: { apiToken: { configured: false, masked: "" } },
  networkProxy: { enabled: false, url: "" },
  agent: {
    enabled: false,
    protocol: "openai_chat_completions",
    baseUrl: "",
    model: "",
    apiKey: { configured: false, masked: "" },
    useNetworkProxy: true,
    requestTimeoutSeconds: 60,
    rssCoordinateMode: "off",
    downloadFileSelectionMode: "off",
    catalogMatchEnabled: false,
    episodeMappingEnabled: false,
    allowAutomaticEpisodeMapping: false,
    subtitleVideoMatchMode: "off",
  },
  paths: {
    downloadRoot: "/data/downloads",
    workRoot: "/data/work",
    stagingRoot: "/data/staging",
    animeLibraryRoot: "/data/library/anime",
    movieLibraryRoot: "/data/library/movies",
    ffmpegPath: "/usr/bin/ffmpeg",
    ffprobePath: "/usr/bin/ffprobe",
  },
  transcode: {
    name: "default",
    videoCodec: "h264",
    encoder: "libx264",
    container: "matroska",
    fileExtension: "mkv",
    qualityMode: "crf",
    qualityValue: 20,
    audioPolicy: "copy",
    preset: "medium",
    pixelFormat: "yuv420p",
    threadCount: 0,
    maxConcurrency: 2,
  },
  events: { retentionDays: 30 },
};

function mockConfig() {
  server.use(
    http.get("*/api/v1/config", () => HttpResponse.json(baseConfiguration)),
    http.post("*/api/v1/config/secrets/reveal", () => HttpResponse.json({
      qbPassword: "qb-real-password",
      embyApiKey: "emby-real-api-key",
    })),
    http.get("*/api/v1/dashboard/summary", () =>
      HttpResponse.json({
        counts: {
          downloading: 0,
          processing: 0,
          awaitingReview: 0,
          importing: 0,
          attention: 0,
          failed: 0,
          cleanupFailed: 0,
          mappingPending: 0,
        },
        attentionItems: [],
        recentOperations: [],
        recentImports: [],
        recentScans: [],
        dependencies: {
          qBittorrent: { configured: true },
          tmdb: { configured: true },
          emby: { configured: true },
          mediaTools: { configured: true },
          networkProxy: { configured: false },
          agent: { configured: false },
        },
        links: {
          downloading: "/acquisitions?phase=downloading",
          processing: "/acquisitions?phase=processing",
          awaitingReview: "/acquisitions?phase=awaiting_review",
          importing: "/acquisitions?phase=importing",
          failed: "/acquisitions?phase=attention",
          cleanupFailed: "/acquisitions?phase=attention",
          mappingPending: "/acquisitions?phase=mapping_pending",
        },
      }),
    ),
  );
}

describe("SettingsHubPage", () => {
  it("shows per-service configuration status and links to each section", async () => {
    mockConfig();
    renderWithProviders(<SettingsHubPage />);

    expect(
      await screen.findByRole("heading", { name: "设置" }),
    ).toBeInTheDocument();
    expect(
      (await screen.findAllByText("qBittorrent")).length,
    ).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("Emby").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("TMDb").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("网络代理").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("已配置").length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText("Token 未配置")).toBeInTheDocument();
    expect(screen.getByText("未启用")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /qBittorrent/ })).toHaveAttribute(
      "href",
      "/settings/services",
    );
    expect(screen.getByRole("link", { name: /转码配置/ })).toHaveAttribute(
      "href",
      "/settings/transcode",
    );
  });
});

describe("SettingsServicesPage", () => {
  const fullyConfigured: Configuration = {
    ...baseConfiguration,
    tmdb: { apiToken: { configured: true, masked: "••••ef" } },
  };

  function mockFullyConfigured() {
    server.use(
      http.get("*/api/v1/config", () => HttpResponse.json(fullyConfigured)),
      http.post("*/api/v1/config/secrets/reveal", () =>
        HttpResponse.json({
          qbPassword: "qb-real-password",
          embyApiKey: "emby-real-api-key",
          tmdbApiToken: "tmdb-real-token",
        }),
      ),
    );
  }

  it("reveals configured secrets as editable real values and keeps them when unchanged", async () => {
    mockConfig();
    mockFullyConfigured();
    let captured: Record<string, unknown> | null = null;
    server.use(
      http.put("*/api/v1/config", async ({ request }) => {
        captured = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ ...baseConfiguration, version: 4 });
      }),
    );

    renderWithProviders(<SettingsServicesPage />);

    // 密钥默认以真实值填入，type=password 显示为 *，无清除/编辑按钮
    const password = await screen.findByLabelText("密码");
    await waitFor(() => expect(password).toHaveValue("qb-real-password"));
    expect(password).toHaveAttribute("type", "password");
    expect(password).toBeEnabled();
    expect(screen.getByLabelText("API key")).toHaveValue("emby-real-api-key");
    expect(screen.getByLabelText("API Read Access Token")).toHaveValue(
      "tmdb-real-token",
    );
    for (const storage of [localStorage, sessionStorage]) {
      const values = Array.from(
        { length: storage.length },
        (_, index) => storage.getItem(storage.key(index) ?? ""),
      ).join(" ");
      expect(values).not.toContain("qb-real-password");
    }
    expect(
      screen.queryByRole("button", { name: "清除" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /更改/ }),
    ).not.toBeInTheDocument();

    // 小眼睛切换明文/密文显示
    await userEvent.click(screen.getByRole("button", { name: "显示密码" }));
    expect(password).toHaveAttribute("type", "text");
    await userEvent.click(screen.getByRole("button", { name: "隐藏密码" }));
    expect(password).toHaveAttribute("type", "password");

    // 未修改时保存 → keep
    await userEvent.click(
      screen.getByRole("button", { name: "保存外部服务配置" }),
    );
    await waitFor(() => expect(captured).not.toBeNull());
    const body = captured as unknown as {
      expectedVersion: number;
      qBittorrent: {
        password: { action: string; value?: string };
        downloadRateLimitKibPerSecond: number;
        uploadRateLimitKibPerSecond: number;
      };
      emby: { apiKey: { action: string; value?: string } };
      tmdb: { apiToken: { action: string; value?: string } };
      networkProxy: { enabled: boolean; url: string };
      paths: { animeLibraryRoot: string; movieLibraryRoot: string };
    };
    expect(body.expectedVersion).toBe(3);
    expect(body.qBittorrent.password.action).toBe("keep");
    expect(body.qBittorrent.password.value).toBeUndefined();
    expect(body.qBittorrent.downloadRateLimitKibPerSecond).toBe(2048);
    expect(body.qBittorrent.uploadRateLimitKibPerSecond).toBe(512);
    expect(body.emby.apiKey.action).toBe("keep");
    expect(body.emby.apiKey.value).toBeUndefined();
    expect(body.tmdb.apiToken.action).toBe("keep");
    expect(body.networkProxy).toEqual({ enabled: false, url: "" });
    expect((body as unknown as { agent: { apiKey: { action: string } } }).agent.apiKey.action).toBe("keep");
    expect(body.paths.animeLibraryRoot).toBe("/data/library/anime");
    expect(body.paths.movieLibraryRoot).toBe("/data/library/movies");
    expect((body as unknown as { events: { retentionDays: number } }).events.retentionDays).toBe(30);
  });

  it("saves modified secret values as set", async () => {
    mockConfig();
    mockFullyConfigured();
    let captured: Record<string, unknown> | null = null;
    server.use(
      http.put("*/api/v1/config", async ({ request }) => {
        captured = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ ...baseConfiguration, version: 4 });
      }),
    );

    renderWithProviders(<SettingsServicesPage />);
    const password = await screen.findByLabelText("密码");
    await waitFor(() => expect(password).toHaveValue("qb-real-password"));

    await userEvent.clear(password);
    await userEvent.type(password, "new-secret");
    await userEvent.click(
      screen.getByRole("button", { name: "保存外部服务配置" }),
    );

    await waitFor(() => expect(captured).not.toBeNull());
    const body = captured as unknown as {
      qBittorrent: { password: { action: string; value?: string } };
    };
    expect(body.qBittorrent.password).toEqual({
      action: "set",
      value: "new-secret",
    });
  });

  it("clears a configured secret by emptying the field", async () => {
    mockConfig();
    mockFullyConfigured();
    let captured: Record<string, unknown> | null = null;
    server.use(
      http.put("*/api/v1/config", async ({ request }) => {
        captured = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ ...baseConfiguration, version: 4 });
      }),
    );

    renderWithProviders(<SettingsServicesPage />);
    const apiKey = await screen.findByLabelText("API key");
    await waitFor(() => expect(apiKey).toHaveValue("emby-real-api-key"));

    await userEvent.clear(apiKey);
    await userEvent.click(
      screen.getByRole("button", { name: "保存外部服务配置" }),
    );
    await waitFor(() => expect(captured).not.toBeNull());
    const body = captured as unknown as {
      emby: { apiKey: { action: string; value?: string } };
    };
    expect(body.emby.apiKey).toEqual({ action: "clear" });
  });

  it("reveals each configured secret when another secret is unconfigured", async () => {
    mockConfig();
    let captured: Record<string, unknown> | null = null;
    server.use(
      http.put("*/api/v1/config", async ({ request }) => {
        captured = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ ...baseConfiguration, version: 4 });
      }),
    );

    renderWithProviders(<SettingsServicesPage />);

    // Configured values are loaded independently; the unconfigured TMDb field remains empty.
    const tmdbToken = await screen.findByLabelText("API Read Access Token");
    expect(tmdbToken).toHaveValue("");
    await waitFor(() => expect(screen.getByLabelText("密码")).toHaveValue("qb-real-password"));
    expect(screen.getByLabelText("API key")).toHaveValue("emby-real-api-key");

    await userEvent.type(tmdbToken, "brand-new-token");
    await userEvent.click(
      screen.getByRole("button", { name: "保存外部服务配置" }),
    );

    await waitFor(() => expect(captured).not.toBeNull());
    const body = captured as unknown as {
      qBittorrent: { password: { action: string; value?: string } };
      emby: { apiKey: { action: string; value?: string } };
      tmdb: { apiToken: { action: string; value?: string } };
    };
    expect(body.qBittorrent.password.action).toBe("keep");
    expect(body.emby.apiKey.action).toBe("keep");
    expect(body.tmdb.apiToken).toEqual({
      action: "set",
      value: "brand-new-token",
    });
  });

  it("tests qBittorrent login without resubmitting legacy rate limits", async () => {
    mockConfig();
    const legacyConfiguration: Configuration = {
      ...baseConfiguration,
      qBittorrent: {
        ...baseConfiguration.qBittorrent,
        downloadRateLimitKibPerSecond: 2097152,
        uploadRateLimitKibPerSecond: 2147483647,
      },
    };
    let captured: Record<string, unknown> | null = null;
    server.use(
      http.get("*/api/v1/config", () => HttpResponse.json(legacyConfiguration)),
      http.post("*/api/v1/config/test", async ({ request }) => {
        captured = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({
          target: "qbittorrent",
          success: true,
          code: "ok",
          message: "connection succeeded",
          checkedAt: "2026-08-16T19:00:00Z",
        });
      }),
    );

    renderWithProviders(<SettingsServicesPage />);
    await waitFor(() =>
      expect(screen.getByLabelText("密码")).toHaveValue("qb-real-password"),
    );
    const heading = screen.getByRole("heading", { name: "qBittorrent" });
    const card = heading.parentElement?.parentElement;
    if (!card) {
      throw new Error("qBittorrent card was not rendered");
    }
    await userEvent.click(
      within(card).getByRole("button", { name: "测试连接" }),
    );

    await waitFor(() => expect(captured).not.toBeNull());
    expect(captured).toEqual({
      target: "qbittorrent",
      qBittorrent: {
        url: "http://qb:8080",
        username: "admin",
        password: { action: "keep" },
      },
    });
  });

  it("adjusts and saves qBittorrent per-torrent rate limits", async () => {
    mockConfig();
    let captured: UpdateConfigurationRequest | null = null;
    server.use(
      http.put("*/api/v1/config", async ({ request }) => {
        captured = (await request.json()) as UpdateConfigurationRequest;
        return HttpResponse.json({
          ...baseConfiguration,
          version: 4,
          qBittorrent: captured.qBittorrent,
        });
      }),
    );

    renderWithProviders(<SettingsServicesPage />);
    const downloadLimit = await screen.findByLabelText("下载速率限制（KiB/s）");
    const uploadLimit = screen.getByLabelText("上传速率限制（KiB/s）");
    expect(downloadLimit).toHaveValue(2048);
    expect(downloadLimit).toHaveAttribute("max", "2097151");
    expect(uploadLimit).toHaveValue(512);
    expect(uploadLimit).toHaveAttribute("max", "2097151");

    await userEvent.clear(downloadLimit);
    await userEvent.type(downloadLimit, "2097152");
    expect(downloadLimit).toHaveValue(2097151);
    await userEvent.clear(downloadLimit);
    await userEvent.type(downloadLimit, "3072");
    await userEvent.clear(uploadLimit);
    await userEvent.type(uploadLimit, "768");
    await userEvent.click(
      screen.getByRole("button", { name: "保存外部服务配置" }),
    );

    await waitFor(() => expect(captured).not.toBeNull());
    const body = captured as unknown as UpdateConfigurationRequest;
    expect(body.qBittorrent.downloadRateLimitKibPerSecond).toBe(3072);
    expect(body.qBittorrent.uploadRateLimitKibPerSecond).toBe(768);
  });

  it("tests the current unsaved network proxy", async () => {
    mockConfig();
    let captured: Record<string, unknown> | null = null;
    server.use(
      http.post("*/api/v1/config/test", async ({ request }) => {
        captured = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({
          target: "network_proxy",
          success: true,
          code: "ok",
          message: "connection succeeded",
          checkedAt: "2026-07-24T09:00:00Z",
        });
      }),
    );

    renderWithProviders(<SettingsServicesPage />);
    const heading = await screen.findByRole("heading", { name: "网络代理" });
    const card = heading.parentElement?.parentElement;
    if (!card) {
      throw new Error("network proxy card was not rendered");
    }
    const proxy = within(card);
    const testConnection = proxy.getByRole("button", { name: "测试连接" });
    expect(testConnection).toBeDisabled();

    await userEvent.click(proxy.getByRole("checkbox", { name: "启用代理" }));
    await userEvent.type(
      proxy.getByLabelText("代理 URL"),
      "http://127.0.0.1:7897",
    );
    expect(testConnection).toBeEnabled();
    await userEvent.click(testConnection);

    await waitFor(() => expect(captured).not.toBeNull());
    expect(captured).toEqual({
      target: "network_proxy",
      networkProxy: { enabled: true, url: "http://127.0.0.1:7897" },
    });
    expect(await proxy.findByRole("status")).toHaveTextContent("连接成功");
  });

  it("enables and saves the external network proxy", async () => {
    mockConfig();
    let captured: Record<string, unknown> | null = null;
    server.use(
      http.put("*/api/v1/config", async ({ request }) => {
        captured = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({
          ...baseConfiguration,
          version: 4,
          networkProxy: { enabled: true, url: "http://127.0.0.1:7890" },
        });
      }),
    );

    renderWithProviders(<SettingsServicesPage />);
    const enabled = await screen.findByRole("checkbox", { name: "启用代理" });
    const proxyUrl = screen.getByLabelText("代理 URL");
    expect(proxyUrl).toBeDisabled();

    await userEvent.click(enabled);
    await userEvent.type(proxyUrl, "http://127.0.0.1:7890");
    await userEvent.click(
      screen.getByRole("button", { name: "保存外部服务配置" }),
    );

    await waitFor(() => expect(captured).not.toBeNull());
    expect(
      (captured as unknown as { networkProxy: unknown }).networkProxy,
    ).toEqual({ enabled: true, url: "http://127.0.0.1:7890" });
  });
});

describe("SettingsAgentPage", () => {
  const configuredAgent: Configuration = {
    ...baseConfiguration,
    agent: {
      ...baseConfiguration.agent,
      enabled: true,
      baseUrl: "https://agent.example/v1",
      model: "fixture-model",
      apiKey: { configured: true, masked: "••••12" },
      episodeMappingEnabled: true,
    },
  };

  function mockAgentConfiguration() {
    server.use(
      http.get("*/api/v1/config", () => HttpResponse.json(configuredAgent)),
    );
  }

  it("reveals the saved API key and uses keep, set, and clear actions consistently", async () => {
    mockConfig();
    mockAgentConfiguration();
    let revealCalls = 0;
    let captured: Record<string, unknown> | null = null;
    server.use(
      http.post("*/api/v1/config/secrets/reveal", () => {
        revealCalls += 1;
        return HttpResponse.json({ qbPassword: "qb", embyApiKey: "emby", tmdbApiToken: "tmdb", agentApiKey: "saved-agent-key" });
      }),
      http.post("*/api/v1/config/test", async ({ request }) => {
        captured = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ target: "agent", success: true, code: "ok", message: "connection succeeded", checkedAt: "2026-08-01T04:00:00Z" });
      }),
    );

    renderWithProviders(<SettingsAgentPage />);
    const key = await screen.findByLabelText("Agent API key");
    await waitFor(() => expect(key).toHaveValue("saved-agent-key"));
    expect(key).toHaveAttribute("type", "password");
    expect(revealCalls).toBe(1);

    await userEvent.click(screen.getByRole("button", { name: "测试连接" }));
    await waitFor(() => expect(captured).not.toBeNull());
    expect(captured).toEqual({
      target: "agent",
      agent: {
        protocol: "openai_chat_completions",
        baseUrl: "https://agent.example/v1",
        model: "fixture-model",
        apiKey: { action: "keep" },
        useNetworkProxy: true,
      },
    });

    captured = null;
    await userEvent.clear(key);
    await userEvent.type(key, "candidate-key");
    await userEvent.click(screen.getByRole("button", { name: "测试连接" }));
    await waitFor(() => expect(captured).not.toBeNull());
    expect(captured).toEqual({
      target: "agent",
      agent: {
        protocol: "openai_chat_completions",
        baseUrl: "https://agent.example/v1",
        model: "fixture-model",
        apiKey: { action: "set", value: "candidate-key" },
        useNetworkProxy: true,
      },
    });
  });

  it("keeps a configured Agent key when reveal is unavailable", async () => {
    mockConfig();
    mockAgentConfiguration();
    let captured: UpdateConfigurationRequest | null = null;
    server.use(
      http.post("*/api/v1/config/secrets/reveal", () => HttpResponse.error()),
      http.put("*/api/v1/config", async ({ request }) => {
        captured = (await request.json()) as UpdateConfigurationRequest;
        return HttpResponse.json({ ...configuredAgent, version: 4 });
      }),
    );

    renderWithProviders(<SettingsAgentPage />);
    const key = await screen.findByLabelText("Agent API key");
    expect(key).toHaveValue("");
    await userEvent.click(screen.getByRole("button", { name: "保存 Agent 配置" }));
    await waitFor(() => expect(captured).not.toBeNull());
    expect((captured as unknown as UpdateConfigurationRequest).agent.apiKey).toEqual({ action: "keep" });
  });

  it("clears the loaded Agent key after Agent assistance is disabled", async () => {
    mockConfig();
    mockAgentConfiguration();
    let captured: UpdateConfigurationRequest | null = null;
    server.use(
      http.post("*/api/v1/config/secrets/reveal", () => HttpResponse.json({
        qbPassword: "qb", embyApiKey: "emby", tmdbApiToken: "tmdb", agentApiKey: "saved-agent-key",
      })),
      http.put("*/api/v1/config", async ({ request }) => {
        captured = (await request.json()) as UpdateConfigurationRequest;
        return HttpResponse.json({ ...configuredAgent, version: 4 });
      }),
    );

    renderWithProviders(<SettingsAgentPage />);
    const key = await screen.findByLabelText("Agent API key");
    await waitFor(() => expect(key).toHaveValue("saved-agent-key"));
    await userEvent.click(screen.getByRole("checkbox", { name: "启用 Agent 辅助" }));
    await userEvent.clear(key);
    await userEvent.click(screen.getByRole("button", { name: "保存 Agent 配置" }));
    await waitFor(() => expect(captured).not.toBeNull());
    const body = captured as unknown as UpdateConfigurationRequest;
    expect(body.agent.apiKey).toEqual({ action: "clear" });
    expect(body.agent).toMatchObject({
      enabled: false,
      rssCoordinateMode: "off",
      downloadFileSelectionMode: "off",
      catalogMatchEnabled: false,
      episodeMappingEnabled: false,
      allowAutomaticEpisodeMapping: false,
    });
  });

  it("uses binary capability switches to allow Agent fallback without bypassing deterministic rules", async () => {
    mockConfig();
    const legacySuggestConfiguration: Configuration = {
      ...configuredAgent,
      agent: {
        ...configuredAgent.agent,
        rssCoordinateMode: "suggest",
        downloadFileSelectionMode: "off",
        catalogMatchEnabled: false,
        episodeMappingEnabled: true,
        allowAutomaticEpisodeMapping: false,
      },
    };
    server.use(
      http.get("*/api/v1/config", () => HttpResponse.json(legacySuggestConfiguration)),
    );
    let captured: UpdateConfigurationRequest | null = null;
    server.use(
      http.put("*/api/v1/config", async ({ request }) => {
        captured = (await request.json()) as UpdateConfigurationRequest;
        return HttpResponse.json({ ...legacySuggestConfiguration, version: 4, agent: captured.agent });
      }),
    );

    renderWithProviders(<SettingsAgentPage />);
    await screen.findByLabelText("Agent API key");
    expect(screen.getByText("确定性规则优先，Agent 仅处理无法唯一判断的异常情况")).toBeInTheDocument();
    expect(screen.getByText("开关仅允许兜底介入，不会跳过确定性规则")).toBeInTheDocument();
    expect(screen.queryByRole("radio")).not.toBeInTheDocument();
    expect(screen.queryByText("建议", { exact: true })).not.toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: "RSS 发布筛选" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "下载文件解析" })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "TMDb 候选辅助" })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "剧集 Mapping" })).toBeChecked();

    await userEvent.click(screen.getByRole("checkbox", { name: "下载文件解析" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "TMDb 候选辅助" }));
    await userEvent.click(screen.getByRole("button", { name: "保存 Agent 配置" }));
    await waitFor(() => expect(captured).not.toBeNull());
    const body = captured as unknown as UpdateConfigurationRequest;
    expect(body.agent).toMatchObject({
      enabled: true,
      rssCoordinateMode: "validated_auto",
      downloadFileSelectionMode: "validated_auto",
      catalogMatchEnabled: true,
      episodeMappingEnabled: true,
      allowAutomaticEpisodeMapping: true,
    });
    expect(body.agent.apiKey).toEqual({ action: "keep" });
  });
});

describe("SettingsStoragePage", () => {
  it("edits and saves storage paths only", async () => {
    mockConfig();
    let captured: UpdateConfigurationRequest | null = null;
    server.use(
      http.put("*/api/v1/config", async ({ request }) => {
        captured = (await request.json()) as UpdateConfigurationRequest;
        return HttpResponse.json({
          ...baseConfiguration,
          version: 4,
          paths: captured.paths,
        });
      }),
    );

    renderWithProviders(<SettingsStoragePage />);
    const downloadRoot = await screen.findByLabelText("下载根目录");
    expect(downloadRoot).toHaveValue("/data/downloads");

    await userEvent.clear(downloadRoot);
    await userEvent.type(downloadRoot, "/srv/emby-auto/downloads-new");
    await userEvent.click(screen.getByRole("button", { name: "保存存储配置" }));

    await waitFor(() => expect(captured).not.toBeNull());
    const body = captured as unknown as UpdateConfigurationRequest;
    expect(body.paths.downloadRoot).toBe("/srv/emby-auto/downloads-new");
    expect(body.paths.ffmpegPath).toBe("/usr/bin/ffmpeg");
    expect(body.qBittorrent.url).toBe("http://qb:8080");
  });
});

describe("SettingsTranscodePage", () => {
  it("offers complete recommended transcode choices and submits the selected profile", async () => {
    mockConfig();
    let captured: UpdateConfigurationRequest | null = null;
    server.use(
      http.put("*/api/v1/config", async ({ request }) => {
        captured = (await request.json()) as UpdateConfigurationRequest;
        return HttpResponse.json({
          ...baseConfiguration,
          version: 4,
          transcode: captured.transcode,
        });
      }),
    );

    renderWithProviders(<SettingsTranscodePage />);
    const fields = await screen.findByRole("group", { name: "转码参数" });

    expect(within(fields).getByLabelText("视频编码")).toHaveRole("combobox");
    expect(within(fields).getByLabelText("编码器")).toHaveRole("combobox");
    expect(within(fields).getByLabelText("质量值")).toHaveRole("combobox");
    expect(within(fields).getByLabelText("preset")).toHaveRole("combobox");
    expect(within(fields).getByLabelText("像素格式")).toHaveRole("combobox");
    expect(within(fields).getByLabelText("线程数")).toHaveRole("combobox");
    expect(within(fields).getByLabelText("转码并发数")).toHaveRole("combobox");

    await userEvent.selectOptions(
      within(fields).getByLabelText("推荐方案"),
      "nvidia-hevc",
    );
    expect(within(fields).getByLabelText("视频编码")).toHaveValue("hevc");
    expect(within(fields).getByLabelText("编码器")).toHaveValue("hevc_nvenc");
    expect(within(fields).getByLabelText("质量模式")).toHaveValue("cq");
    expect(within(fields).getByLabelText("质量值")).toHaveValue("23");
    expect(within(fields).getByLabelText("preset")).toHaveValue("p4");
    expect(within(fields).getByLabelText("像素格式")).toHaveValue("p010le");
    expect(within(fields).getByLabelText("文件扩展名")).toHaveValue("mkv");
    expect(within(fields).getAllByText(/需要 NVIDIA GPU/)).toHaveLength(2);

    await userEvent.click(screen.getByRole("button", { name: "保存转码配置" }));
    await waitFor(() => expect(captured).not.toBeNull());
    const body = captured as unknown as UpdateConfigurationRequest;
    expect(body.transcode).toEqual({
      name: "default",
      videoCodec: "hevc",
      encoder: "hevc_nvenc",
      container: "matroska",
      fileExtension: "mkv",
      qualityMode: "cq",
      qualityValue: 23,
      audioPolicy: "copy",
      preset: "p4",
      pixelFormat: "p010le",
      threadCount: 0,
      maxConcurrency: 1,
    });
  });

  it("keeps codec, container, audio and quality selections compatible", async () => {
    mockConfig();
    renderWithProviders(<SettingsTranscodePage />);
    const fields = await screen.findByRole("group", { name: "转码参数" });

    await userEvent.selectOptions(
      within(fields).getByLabelText("视频编码"),
      "av1",
    );
    expect(within(fields).getByLabelText("编码器")).toHaveValue("libsvtav1");
    expect(within(fields).getByLabelText("质量模式")).toHaveValue("crf");
    expect(within(fields).getByLabelText("质量值")).toHaveValue("28");
    expect(within(fields).getByLabelText("preset")).toHaveValue("6");

    await userEvent.selectOptions(
      within(fields).getByLabelText("封装格式"),
      "webm",
    );
    expect(within(fields).getByLabelText("文件扩展名")).toHaveValue("webm");
    await userEvent.selectOptions(
      within(fields).getByLabelText("音频策略"),
      "transcode",
    );
    expect(within(fields).getByLabelText("音频编码")).toHaveValue("opus");
    expect(within(fields).getByLabelText("音频编码")).not.toHaveTextContent(
      "aac",
    );
  });

  it("saves modified event retention days", async () => {
    mockConfig();
    let captured: Record<string, unknown> | null = null;
    server.use(
      http.put("*/api/v1/config", async ({ request }) => {
        captured = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ ...baseConfiguration, version: 4 });
      }),
    );

    renderWithProviders(<SettingsServicesPage />);
    const retention = await screen.findByLabelText("保留天数");
    expect(retention).toHaveValue(30);

    await userEvent.clear(retention);
    await userEvent.type(retention, "90");
    await userEvent.click(
      screen.getByRole("button", { name: "保存外部服务配置" }),
    );

    await waitFor(() => expect(captured).not.toBeNull());
    const body = captured as unknown as { events: { retentionDays: number } };
    expect(body.events.retentionDays).toBe(90);
  });
});
