import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Check, LoaderCircle, Pencil, Plus, Trash2, X } from 'lucide-react';
import { useState, type FormEvent } from 'react';

import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { ErrorState } from '@/components/ui/feedback';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { DetailErrorState, DetailLoadingState, PageBody, PageHeader } from '@/components/resource';
import {
  createSubtitleGroup,
  deleteSubtitleGroup,
  fetchSubtitleGroups,
  updateSubtitleGroup,
} from '@/features/rss/api';

export function SettingsSubtitleGroupsPage() {
  const groups = useQuery({ queryKey: ['rss-subtitle-groups'], queryFn: fetchSubtitleGroups });
  const queryClient = useQueryClient();
  const [name, setName] = useState('');
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editingName, setEditingName] = useState('');

  const save = useMutation({
    mutationFn: () => editingId ? updateSubtitleGroup(editingId, { name: editingName.trim() }) : createSubtitleGroup(name.trim()),
    onSuccess: () => {
      setName('');
      setEditingId(null);
      setEditingName('');
      void queryClient.invalidateQueries({ queryKey: ['rss-subtitle-groups'] });
    },
  });
  const remove = useMutation({
    mutationFn: deleteSubtitleGroup,
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ['rss-subtitle-groups'] }),
  });

  if (groups.isPending) {
    return <DetailLoadingState title="字幕组列表" label="正在读取字幕组" />;
  }
  if (groups.error || !groups.data) {
    return <DetailErrorState title="字幕组列表" message={groups.error?.message ?? '无法读取字幕组列表'} onRetry={() => groups.refetch()} />;
  }

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (editingId ? !editingName.trim() : !name.trim()) return;
    save.mutate();
  };

  return (
    <PageBody>
      <PageHeader title="字幕组列表" description="RSS 订阅名称识别" />
      <Card>
        <CardHeader>
          <CardTitle>已维护的字幕组</CardTitle>
          <CardDescription>添加、修改或删除用于 RSS 识别的名称</CardDescription>
        </CardHeader>
        <CardContent className="space-y-5">
          <form className="flex flex-col gap-2 sm:flex-row sm:items-end" onSubmit={submit}>
            <div className="min-w-0 flex-1 space-y-2">
              <Label htmlFor="rss-subtitle-group-name">字幕组名称</Label>
              <Input
                id="rss-subtitle-group-name"
                value={editingId ? editingName : name}
                onChange={(event) => (editingId ? setEditingName(event.target.value) : setName(event.target.value))}
                placeholder="输入字幕组名称"
                maxLength={128}
              />
            </div>
            <div className="flex gap-2">
              <Button type="submit" disabled={save.isPending || !(editingId ? editingName.trim() : name.trim())}>
                {save.isPending ? <LoaderCircle className="animate-spin" aria-hidden="true" /> : editingId ? <Check aria-hidden="true" /> : <Plus aria-hidden="true" />}
                {editingId ? '保存名称' : '添加字幕组'}
              </Button>
              {editingId ? (
                <Button type="button" variant="outline" aria-label="取消编辑" title="取消编辑" onClick={() => { setEditingId(null); setEditingName(''); }}>
                  <X aria-hidden="true" />
                </Button>
              ) : null}
            </div>
          </form>

          {save.error ? <ErrorState message={save.error instanceof Error ? save.error.message : '保存字幕组失败'} /> : null}
          {remove.error ? <ErrorState message={remove.error instanceof Error ? remove.error.message : '删除字幕组失败'} /> : null}

          {groups.data.items.length > 0 ? (
            <ul className="divide-y divide-zinc-100 border-y border-zinc-200">
              {groups.data.items.map((group) => (
                <li key={group.id} className="flex items-center justify-between gap-3 py-3">
                  <span className="min-w-0 break-words text-sm font-medium text-zinc-900">{group.name}</span>
                  <span className="flex shrink-0 gap-1">
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      aria-label={`编辑 ${group.name}`}
                      title="编辑"
                      onClick={() => { setEditingId(group.id); setEditingName(group.name); save.reset(); }}
                    >
                      <Pencil aria-hidden="true" />
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      aria-label={`删除 ${group.name}`}
                      title="删除"
                      disabled={remove.isPending}
                      onClick={() => remove.mutate(group.id)}
                    >
                      <Trash2 aria-hidden="true" />
                    </Button>
                  </span>
                </li>
              ))}
            </ul>
          ) : (
            <p className="border border-dashed border-zinc-300 px-4 py-8 text-center text-sm text-zinc-500">暂未维护字幕组</p>
          )}
        </CardContent>
      </Card>
    </PageBody>
  );
}
