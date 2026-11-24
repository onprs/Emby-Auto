import { createContext, useContext, type ReactNode } from 'react';

const FieldHintContext = createContext<string | undefined>(undefined);

/**
 * 把字段说明文本的 id 交给字段内部的控件。说明只有挂在真正可聚焦的控件上，
 * 辅助技术才会在控件获得焦点时朗读它。
 */
export function FieldHintProvider({ id, children }: { id?: string; children: ReactNode }) {
  return <FieldHintContext.Provider value={id}>{children}</FieldHintContext.Provider>;
}

/** 当前字段说明文本的 id；字段没有说明时为 undefined。 */
export function useFieldHintId(): string | undefined {
  return useContext(FieldHintContext);
}
