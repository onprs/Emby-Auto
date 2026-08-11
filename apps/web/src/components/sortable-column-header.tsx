import { ArrowDown, ArrowUp, ChevronsUpDown } from 'lucide-react';

export type SortOrder = 'asc' | 'desc';

export function SortableColumnHeader<TField extends string>({
  label,
  field,
  activeField,
  order,
  onSort,
}: {
  label: string;
  field: TField;
  activeField?: TField;
  order?: SortOrder;
  onSort: (field: TField) => void;
}) {
  const active = activeField === field;
  const nextOrder: SortOrder = active && order === 'asc' ? 'desc' : 'asc';
  const Icon = active ? (order === 'desc' ? ArrowDown : ArrowUp) : ChevronsUpDown;
  return (
    <button
      type="button"
      className="inline-flex min-h-8 items-center gap-1.5 whitespace-nowrap font-medium text-zinc-600 hover:text-zinc-950 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-emerald-600/30"
      aria-label={`${label}，点击按${nextOrder === 'asc' ? '正序' : '逆序'}排列`}
      aria-pressed={active}
      title={`按${label}${nextOrder === 'asc' ? '正序' : '逆序'}排列`}
      onClick={() => onSort(field)}
    >
      <span>{label}</span>
      <Icon className={`size-3.5 ${active ? 'text-emerald-700' : 'text-zinc-400'}`} aria-hidden="true" />
    </button>
  );
}
