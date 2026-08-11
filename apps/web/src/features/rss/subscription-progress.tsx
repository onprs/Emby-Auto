import type { RssSubscription } from '@/api/generated/types.gen';
import { OverallProgressBar, type OverallProgressTone } from '@/components/overall-progress';

export function SubscriptionProgress({ subscription, compact = false }: { subscription: RssSubscription; compact?: boolean }) {
  const presentation = subscriptionProgressPresentation(subscription);
  return (
    <OverallProgressBar
      value={subscription.completedAt ? 1 : subscription.overallProgress}
      label={presentation.label}
      ariaLabel={`${subscription.name}总进度`}
      tone={presentation.tone}
      compact={compact}
    />
  );
}

export function subscriptionProgressPresentation(subscription: RssSubscription): { label: string; tone: OverallProgressTone } {
  if (subscription.completedAt) {
    return { label: '订阅已完成', tone: 'complete' };
  }
  const pausedPrefix = subscription.enabled ? '' : '已暂停 · ';
  if (subscription.taskCount === 0) {
    return { label: `${pausedPrefix}等待发现内容`, tone: 'neutral' };
  }
  if (subscription.attentionTaskCount > 0) {
    return {
      label: `${pausedPrefix}${subscription.attentionTaskCount} 个需处理 · ${subscription.completedTaskCount} / ${subscription.taskCount} 已完成`,
      tone: 'attention',
    };
  }
  if (subscription.completedTaskCount === subscription.taskCount) {
    return { label: `${pausedPrefix}${subscription.taskCount} 个任务已完成`, tone: 'complete' };
  }
  return {
    label: `${pausedPrefix}${subscription.completedTaskCount} / ${subscription.taskCount} 个任务已完成`,
    tone: subscription.enabled ? 'active' : 'neutral',
  };
}
