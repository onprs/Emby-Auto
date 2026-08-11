import {
  Film,
  LayoutDashboard,
  ListChecks,
  Rss,
  Search,
  ScrollText,
  Settings,
  type LucideIcon,
} from 'lucide-react';

import type { Session } from '@/api/generated/types.gen';

export type AppModuleId = 'dashboard' | 'searches' | 'rss' | 'tasks' | 'emby' | 'operations' | 'settings';

export interface AppNavigationItem {
  id: AppModuleId;
  label: string;
  shortLabel: string;
  to: string;
  icon: LucideIcon;
  children?: Array<{ label: string; to: string }>;
  visible: (session: Session) => boolean;
}

const authenticated = (_session: Session) => true;

/** The only top-level navigation definition used by desktop and mobile layouts. */
export const appNavigation: AppNavigationItem[] = [
  { id: 'dashboard', label: '仪表盘', shortLabel: '仪表盘', to: '/', icon: LayoutDashboard, visible: authenticated },
  { id: 'searches', label: '搜索添加', shortLabel: '搜索', to: '/searches', icon: Search, visible: authenticated },
  { id: 'rss', label: 'RSS 订阅', shortLabel: 'RSS', to: '/rss', icon: Rss, visible: authenticated },
  { id: 'tasks', label: '任务', shortLabel: '任务', to: '/acquisitions', icon: ListChecks, visible: authenticated },
  { id: 'emby', label: '媒体库', shortLabel: '媒体库', to: '/emby', icon: Film, visible: authenticated },
  {
    id: 'operations', label: '运行记录', shortLabel: '运行', to: '/operations', icon: ScrollText,
    children: [
      { label: '任务执行', to: '/operations' },
    ],
    visible: authenticated,
  },
  {
    id: 'settings',
    label: '系统设置',
    shortLabel: '设置',
    to: '/settings',
    icon: Settings,
    children: [
      { label: '总览', to: '/settings' },
      { label: '外部服务', to: '/settings/services' },
      { label: 'Agent', to: '/settings/agent' },
      { label: '存储与工具', to: '/settings/storage' },
      { label: '转码', to: '/settings/transcode' },
    ],
    visible: authenticated,
  },
];

export interface AppRouteRecord {
  id: string;
  pattern: RegExp;
  module: AppModuleId;
  pageLabel: string;
  detail: boolean;
  defaultReturn?: string | ((pathname: string) => string);
}

const appRoutes: AppRouteRecord[] = [
  { id: 'dashboard', pattern: /^\/$/, module: 'dashboard', pageLabel: '仪表盘', detail: false },
  { id: 'search-detail', pattern: /^\/searches\/[^/]+\/?$/, module: 'searches', pageLabel: '搜索详情', detail: true, defaultReturn: '/searches' },
  { id: 'searches', pattern: /^\/searches\/?$/, module: 'searches', pageLabel: '搜索添加', detail: false },
  { id: 'rss-detail', pattern: /^\/rss\/[^/]+\/?$/, module: 'rss', pageLabel: '订阅详情', detail: true, defaultReturn: '/rss' },
  { id: 'rss', pattern: /^\/rss\/?$/, module: 'rss', pageLabel: 'RSS 订阅', detail: false },
  {
    id: 'acquisition-mapping', pattern: /^\/acquisitions\/[^/]+\/mapping\/?$/, module: 'tasks', pageLabel: '剧集对应关系', detail: true,
    defaultReturn: (pathname) => pathname.replace(/\/mapping\/?$/, ''),
  },
  { id: 'acquisition-detail', pattern: /^\/acquisitions\/[^/]+\/?$/, module: 'tasks', pageLabel: '内容任务详情', detail: true, defaultReturn: '/acquisitions' },
  { id: 'acquisitions', pattern: /^\/acquisitions\/?$/, module: 'tasks', pageLabel: '任务', detail: false },
  { id: 'task-detail', pattern: /^\/tasks\/[^/]+\/?$/, module: 'tasks', pageLabel: '媒体处理诊断', detail: true, defaultReturn: '/acquisitions' },
  { id: 'tasks', pattern: /^\/tasks\/?$/, module: 'tasks', pageLabel: '任务', detail: false, defaultReturn: '/acquisitions' },
  { id: 'download-detail', pattern: /^\/downloads\/[^/]+\/?$/, module: 'tasks', pageLabel: '下载诊断', detail: true, defaultReturn: '/acquisitions' },
  { id: 'downloads', pattern: /^\/downloads\/?$/, module: 'tasks', pageLabel: '任务', detail: false, defaultReturn: '/acquisitions' },
  { id: 'emby-scan-detail', pattern: /^\/emby\/scans\/[^/]+\/?$/, module: 'emby', pageLabel: '目录更新详情', detail: true, defaultReturn: '/emby' },
  { id: 'emby-library-detail', pattern: /^\/emby\/libraries\/[^/]+\/?$/, module: 'emby', pageLabel: '媒体库条目', detail: true, defaultReturn: '/emby' },
  { id: 'emby', pattern: /^\/emby\/?$/, module: 'emby', pageLabel: '媒体库', detail: false },
  { id: 'operation-detail', pattern: /^\/operations\/[^/]+\/?$/, module: 'operations', pageLabel: '运行详情', detail: true, defaultReturn: '/operations' },
  { id: 'operations', pattern: /^\/operations\/?$/, module: 'operations', pageLabel: '运行记录', detail: false },
  { id: 'settings-services', pattern: /^\/settings\/services\/?$/, module: 'settings', pageLabel: '外部服务设置', detail: true, defaultReturn: '/settings' },
  { id: 'settings-agent', pattern: /^\/settings\/agent\/?$/, module: 'settings', pageLabel: 'Agent 设置', detail: true, defaultReturn: '/settings' },
  { id: 'settings-storage', pattern: /^\/settings\/storage\/?$/, module: 'settings', pageLabel: '存储与工具设置', detail: true, defaultReturn: '/settings' },
  { id: 'settings-transcode', pattern: /^\/settings\/transcode\/?$/, module: 'settings', pageLabel: '转码设置', detail: true, defaultReturn: '/settings' },
  { id: 'settings', pattern: /^\/settings\/?$/, module: 'settings', pageLabel: '系统设置', detail: false },
  { id: 'forbidden', pattern: /^\/forbidden\/?$/, module: 'dashboard', pageLabel: '无权访问', detail: true, defaultReturn: '/' },
];

export interface AppBreadcrumb {
  label: string;
  to?: string;
}

export function routeForPathname(pathname: string): AppRouteRecord | undefined {
  return appRoutes.find((route) => route.pattern.test(pathname));
}

export function moduleForPathname(pathname: string): AppModuleId | undefined {
  return routeForPathname(pathname)?.module;
}

export function navigationForModule(moduleId?: AppModuleId): AppNavigationItem | undefined {
  return appNavigation.find((item) => item.id === moduleId);
}

export function defaultReturnForPathname(pathname: string): string {
  const route = routeForPathname(pathname);
  if (!route?.defaultReturn) {
    return navigationForModule(route?.module)?.to ?? '/';
  }
  return typeof route.defaultReturn === 'function' ? route.defaultReturn(pathname) : route.defaultReturn;
}

function appOrigin(explicit?: string): string {
  if (explicit) return explicit;
  return typeof window === 'undefined' ? 'http://localhost' : window.location.origin;
}

/** Accepts only known same-origin application routes and returns path + query. */
export function normalizeInternalLocation(value?: string | null, origin?: string): string | undefined {
  if (!value || value.length > 8_192 || value.startsWith('//')) {
    return undefined;
  }
  try {
    const expectedOrigin = appOrigin(origin);
    const parsed = new URL(value, expectedOrigin);
    if (parsed.origin !== expectedOrigin || parsed.username || parsed.password || !routeForPathname(parsed.pathname)) {
      return undefined;
    }
    return `${parsed.pathname}${parsed.search}`;
  } catch {
    return undefined;
  }
}

export function breadcrumbsForLocation(pathname: string, source?: string): AppBreadcrumb[] {
  const current = routeForPathname(pathname);
  if (!current) return [];

  const normalizedSource = normalizeInternalLocation(source);
  const sourceURL = normalizedSource ? new URL(normalizedSource, appOrigin()) : undefined;
  const sourceRoute = sourceURL ? routeForPathname(sourceURL.pathname) : undefined;
  const sourceModule = navigationForModule(sourceRoute?.module);
  const currentModule = navigationForModule(current.module);
  const crumbs: AppBreadcrumb[] = [];

  if (sourceRoute && sourceModule && sourceURL?.pathname !== pathname) {
    const sourceIsModuleRoot = sourceURL?.pathname === sourceModule.to;
    crumbs.push({
      label: sourceModule.label,
      to: sourceIsModuleRoot && !sourceRoute.detail ? normalizedSource : sourceModule.to,
    });
    if (!sourceIsModuleRoot) {
      crumbs.push({ label: sourceRoute.pageLabel, to: normalizedSource });
    }
  } else if (currentModule) {
    crumbs.push({ label: currentModule.label, to: current.detail ? currentModule.to : undefined });
    if (current.detail) {
      const parent = defaultReturnForPathname(pathname);
      const parentURL = new URL(parent, appOrigin());
      const parentRoute = routeForPathname(parentURL.pathname);
      if (parentRoute && parentRoute.module === current.module && parentURL.pathname !== currentModule.to) {
        crumbs.push({ label: parentRoute.pageLabel, to: parent });
      }
    }
  }

  if (current.detail || (sourceRoute && sourceURL?.pathname !== pathname)) {
    crumbs.push({ label: current.pageLabel });
  }
  return deduplicateBreadcrumbs(crumbs);
}

function deduplicateBreadcrumbs(crumbs: AppBreadcrumb[]): AppBreadcrumb[] {
  return crumbs.filter((crumb, index) => index === 0 || crumb.label !== crumbs[index - 1].label || crumb.to !== crumbs[index - 1].to);
}
