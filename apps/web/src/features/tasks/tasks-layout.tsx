import { ListChecks } from 'lucide-react';

import { SectionTabsLayout } from '@/components/section-tabs';

export const taskSections = [
  { id: 'tasks', label: '视频任务', to: '/tasks' },
  { id: 'acquisitions', label: '获取记录', to: '/acquisitions' },
  { id: 'downloads', label: '下载', to: '/downloads' },
] as const;

export const taskNavigation = {
  moduleLabel: '任务',
  moduleIcon: ListChecks,
  ariaLabel: '任务',
  sections: taskSections,
} as const;

export function TasksLayout() {
  return <SectionTabsLayout {...taskNavigation} />;
}
