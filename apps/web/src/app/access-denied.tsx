import { Link } from '@tanstack/react-router';
import { ShieldX } from 'lucide-react';
import { useState } from 'react';

import { lastValidAppLocation } from '@/app/navigation-context';
import { PageBody, PageHeader } from '@/components/resource';
import { Button } from '@/components/ui/button';

export function AccessDeniedPage() {
  const [previous] = useState(lastValidAppLocation);

  return (
    <PageBody>
      <PageHeader title="无权访问" description="当前管理员会话没有访问此页面所需的权限" />
      <section className="mx-auto max-w-lg border-y border-zinc-200 py-10 text-center">
        <ShieldX className="mx-auto size-10 text-red-600" aria-hidden="true" />
        <p className="mt-4 text-sm text-zinc-600">请返回上一有效页面，或回到仪表盘继续操作。</p>
        <div className="mt-6 flex flex-wrap justify-center gap-2">
          {previous !== '/' && previous !== '/forbidden' ? (
            <Button asChild variant="outline"><Link to={previous as never}>返回上一有效页面</Link></Button>
          ) : null}
          <Button asChild><Link to="/">返回仪表盘</Link></Button>
        </div>
      </section>
    </PageBody>
  );
}
