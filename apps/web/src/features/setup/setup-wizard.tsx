import { useMutation } from '@tanstack/react-query';
import {
  ArrowLeft,
  ArrowRight,
  Check,
  Database,
  FileCog,
  FolderCog,
  LoaderCircle,
  Plug,
  ShieldCheck,
  UserRound,
} from 'lucide-react';
import { useState, type FormEvent } from 'react';

import { ApiFailure, submitSetup } from '@/api/app-client';
import type { SetupStatus } from '@/api/generated/types.gen';
import { Button } from '@/components/ui/button';
import { buildSetupRequest, defaultSetupForm, validateSetupStep } from '@/features/setup/setup-form';
import { SetupStepContent } from '@/features/setup/setup-steps';
import { cn } from '@/lib/utils';

type SetupWizardProps = {
  status: SetupStatus;
  onCompleted: (status: SetupStatus) => void;
};

const steps = [
  { label: '数据库', icon: Database },
  { label: '管理员', icon: UserRound },
  { label: '服务', icon: Plug },
  { label: '存储', icon: FolderCog },
  { label: '转码', icon: FileCog },
  { label: '确认', icon: ShieldCheck },
] as const;

const finalStep = steps.length - 1;

export function SetupWizard({ status, onCompleted }: SetupWizardProps) {
  const firstStep = status.databaseManagedExternally ? 1 : 0;
  const [step, setStep] = useState(firstStep);
  const [form, setForm] = useState(defaultSetupForm);
  const [validationError, setValidationError] = useState('');
  const [submissionError, setSubmissionError] = useState('');

  const mutation = useMutation({
    mutationFn: () => submitSetup(buildSetupRequest(form, status.databaseManagedExternally)),
    onSuccess: onCompleted,
    onError: (cause) => {
      setSubmissionError(formatSubmissionError(cause));
      const failedStep = submissionErrorStep(cause);
      if (failedStep !== null) {
        setStep(Math.max(firstStep, failedStep));
      }
      setForm((current) => ({
        ...current,
        database: { ...current.database, password: '' },
        administrator: { ...current.administrator, password: '', confirmation: '' },
        services: {
          ...current.services,
          qbPassword: '',
          embyApiKey: '',
          tmdbApiToken: '',
        },
      }));
    },
  });

  function continueStep() {
    const error = validateSetupStep(step, status.databaseManagedExternally, form);
    if (error) {
      setValidationError(error);
      return;
    }
    setValidationError('');
    setSubmissionError('');
    setStep((current) => Math.min(finalStep, current + 1));
  }

  function previousStep() {
    setValidationError('');
    setSubmissionError('');
    setStep((current) => Math.max(firstStep, current - 1));
  }

  function initialize(event: FormEvent) {
    event.preventDefault();
    for (let candidate = firstStep; candidate < finalStep; candidate += 1) {
      const error = validateSetupStep(candidate, status.databaseManagedExternally, form);
      if (error) {
        setValidationError(error);
        setSubmissionError('');
        setStep(candidate);
        return;
      }
    }
    setValidationError('');
    setSubmissionError('');
    mutation.mutate();
  }

  return (
    <main className="min-h-screen bg-surface px-4 py-6 sm:px-8 sm:py-10">
      <section className="mx-auto w-full max-w-5xl animate-fade-in-up rounded-2xl border border-zinc-200/90 bg-white px-5 shadow-card sm:px-8">
        <header className="flex items-center justify-between border-b border-zinc-200 py-5">
          <div className="min-w-0">
            <p className="text-sm font-semibold text-emerald-700">Emby Auto</p>
            <h1 className="mt-1 text-2xl font-semibold tracking-tight text-zinc-950">系统初始化</h1>
          </div>
          <span className="shrink-0 rounded-full bg-amber-100 px-3 py-1 text-xs font-medium text-amber-900 ring-1 ring-amber-200">待完成</span>
        </header>

        <ol className="grid grid-cols-6 border-b border-zinc-200" aria-label="初始化进度">
          {steps.map((item, index) => {
            const Icon = item.icon;
            const complete = index < step || (index === 0 && status.databaseManagedExternally);
            const active = index === step;
            return (
              <li
                key={item.label}
                className={cn(
                  'flex h-16 min-w-0 items-center justify-center gap-1 border-b-2 px-1 text-xs font-medium sm:gap-2 sm:px-2 sm:text-sm',
                  active ? 'border-emerald-600 text-emerald-800' : 'border-transparent text-zinc-500',
                )}
                aria-current={active ? 'step' : undefined}
              >
                {complete ? <Check className="size-4 shrink-0" /> : <Icon className="size-4 shrink-0" />}
                <span className="hidden truncate sm:inline">{item.label}</span>
              </li>
            );
          })}
        </ol>

        <form onSubmit={initialize} className="py-7 sm:py-9">
          <div className="min-h-[480px]">
            <SetupStepContent
              step={step}
              databaseManagedExternally={status.databaseManagedExternally}
              form={form}
              setForm={setForm}
              disabled={mutation.isPending}
            />
          </div>

          <div className="mt-6 min-h-6" aria-live="polite">
            {validationError ? <p className="text-sm text-red-700">{validationError}</p> : null}
            {submissionError ? <p className="text-sm text-red-700">{submissionError}</p> : null}
          </div>

          <footer className="mt-4 flex items-center justify-between border-t border-zinc-200 py-5">
            <Button type="button" variant="ghost" onClick={previousStep} disabled={step === firstStep || mutation.isPending}>
              <ArrowLeft />
              返回
            </Button>
            {step < finalStep ? (
              <Button type="button" onClick={continueStep} disabled={mutation.isPending}>
                继续
                <ArrowRight />
              </Button>
            ) : (
              <Button type="submit" disabled={mutation.isPending}>
                {mutation.isPending ? <LoaderCircle className="animate-spin" /> : <ShieldCheck />}
                {mutation.isPending ? '初始化中' : '开始初始化'}
              </Button>
            )}
          </footer>
        </form>
      </section>
    </main>
  );
}

function submissionErrorStep(cause: unknown): number | null {
  if (!(cause instanceof ApiFailure) || typeof cause.details.field !== 'string') {
    return null;
  }
  const field = cause.details.field.toLowerCase();
  if (field === 'database' || field.startsWith('database.')) {
    return 0;
  }
  if (field.startsWith('administrator.')) {
    return 1;
  }
  if (field.startsWith('qbittorrent.') || field.startsWith('emby.') || field.startsWith('tmdb.')) {
    return 2;
  }
  if (field.startsWith('paths.')) {
    return 3;
  }
  if (field.startsWith('transcode.')) {
    return 4;
  }
  return null;
}

function formatSubmissionError(cause: unknown): string {
  if (cause instanceof ApiFailure) {
    const field = typeof cause.details.field === 'string' ? cause.details.field : '';
    const reason = typeof cause.details.reason === 'string' ? cause.details.reason : '';
    const detail = field && reason ? `：${field} ${reason}` : '';
    const request = cause.requestId ? `（请求 ${cause.requestId}）` : '';
    return `${cause.message}${detail}${request}`;
  }
  return cause instanceof Error ? cause.message : '初始化失败';
}
