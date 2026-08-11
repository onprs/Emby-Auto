import { Link } from '@tanstack/react-router';
import { FileQuestion } from 'lucide-react';
import { useState } from 'react';

import { lastValidAppLocation } from '@/app/navigation-context';
import { PageBody } from '@/components/resource';
import { Button } from '@/components/ui/button';

export function NotFoundPage() {
  const [previous] = useState(lastValidAppLocation);

  return (
    <PageBody>
      <section className="grid min-h-[60vh] place-items-center py-12">
        <div className="max-w-lg text-center">
          <FileQuestion className="mx-auto size-10 text-zinc-400" aria-hidden="true" />
          <p className="mt-4 text-xs font-medium text-zinc-500">404</p>
          <h1 className="mt-1 text-xl font-semibold text-zinc-950">页面不存在</h1>
          <p className="mt-2 text-sm text-zinc-500">地址可能已失效，或者对应内容已经被删除。</p>
          <div className="mt-6 flex flex-wrap justify-center gap-2">
            {previous !== '/' ? (
              <Button asChild variant="outline"><Link to={previous as never}>返回上一有效页面</Link></Button>
            ) : null}
            <Button asChild><Link to="/">返回仪表盘</Link></Button>
          </div>
        </div>
      </section>
    </PageBody>
  );
}
