import { Settings } from 'lucide-react';

import { SectionTabsLayout } from '@/components/section-tabs';

export const settingsSections = [
  { id: 'services', label: '外部服务', to: '/settings/services' },
  { id: 'agent', label: 'Agent', to: '/settings/agent' },
  { id: 'storage', label: '存储与工具', to: '/settings/storage' },
  { id: 'subtitle-groups', label: '字幕组', to: '/settings/subtitle-groups' },
  { id: 'transcode', label: '转码', to: '/settings/transcode' },
] as const;

export const settingsNavigation = {
  moduleLabel: '系统设置',
  moduleIcon: Settings,
  ariaLabel: '系统设置',
  sections: settingsSections,
  preserveFrom: true,
} as const;

export function SettingsLayout() {
  return <SectionTabsLayout {...settingsNavigation} />;
}
