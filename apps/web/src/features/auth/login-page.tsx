import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Activity, LoaderCircle, LogIn } from 'lucide-react';
import { useState, type FormEvent } from 'react';

import { createSession } from '@/api/app-client';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';

export function LoginPage() {
  const queryClient = useQueryClient();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const mutation = useMutation({
    mutationFn: () => createSession(username, password),
    onSuccess: (session) => queryClient.setQueryData(['session'], session),
  });

  function submit(event: FormEvent) {
    event.preventDefault();
    mutation.mutate();
  }

  return (
    <main className="flex min-h-screen items-center justify-center bg-gradient-to-br from-zinc-100 via-surface to-emerald-50 px-4 py-8">
      <section className="w-full max-w-sm min-w-0 animate-fade-in-up rounded-2xl border border-zinc-200/80 bg-white px-6 py-8 shadow-xl sm:px-8">
        <header className="mb-8 text-center">
          <span className="mx-auto grid size-12 place-items-center rounded-2xl bg-emerald-600 shadow-lg shadow-emerald-600/25">
            <Activity className="size-6 text-white" aria-hidden="true" />
          </span>
          <p className="mt-4 text-sm font-semibold text-emerald-700">Emby Auto</p>
          <h1 className="mt-1 text-2xl font-semibold tracking-tight text-zinc-950">管理员登录</h1>
          <p className="mt-1 text-sm text-zinc-500">登录后管理媒体自动化流水线</p>
        </header>
        <form onSubmit={submit} className="grid gap-5">
          <div className="grid gap-2">
            <Label htmlFor="login-username">用户名</Label>
            <Input id="login-username" value={username} onChange={(event) => setUsername(event.target.value)} autoComplete="username" required />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="login-password">密码</Label>
            <Input id="login-password" type="password" value={password} onChange={(event) => setPassword(event.target.value)} autoComplete="current-password" required />
          </div>
          <div className="min-h-5" aria-live="polite">
            {mutation.error && <p className="animate-fade-in text-sm text-red-700">{mutation.error.message}</p>}
          </div>
          <Button type="submit" variant="accent" className="w-full" disabled={mutation.isPending}>
            {mutation.isPending ? <LoaderCircle className="animate-spin" /> : <LogIn />}
            {mutation.isPending ? '登录中' : '登录'}
          </Button>
        </form>
      </section>
    </main>
  );
}
