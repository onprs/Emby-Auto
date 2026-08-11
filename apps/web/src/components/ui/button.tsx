import { Slot } from '@radix-ui/react-slot';
import { cva, type VariantProps } from 'class-variance-authority';
import { forwardRef, type ButtonHTMLAttributes } from 'react';

import { cn } from '@/lib/utils';

const buttonVariants = cva(
  'inline-flex h-10 shrink-0 cursor-pointer items-center justify-center gap-2 rounded-lg px-4 text-sm font-medium transition-all duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-emerald-600 focus-visible:ring-offset-2 active:scale-[0.98] disabled:pointer-events-none disabled:opacity-50 [&_svg]:size-4',
  {
    variants: {
      variant: {
        default: 'bg-zinc-900 text-white shadow-sm hover:bg-zinc-700 hover:shadow',
        accent: 'bg-emerald-600 text-white shadow-sm shadow-emerald-600/20 hover:bg-emerald-500 hover:shadow-md hover:shadow-emerald-600/25',
        outline: 'border border-zinc-300 bg-white text-zinc-800 shadow-sm hover:border-zinc-400 hover:bg-zinc-50',
        ghost: 'text-zinc-700 hover:bg-zinc-100',
      },
      size: {
        default: 'h-10 px-4',
        sm: 'h-8 rounded-md px-2.5 text-xs',
        icon: 'size-10 px-0',
      },
    },
    defaultVariants: { variant: 'default', size: 'default' },
  },
);

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> &
  VariantProps<typeof buttonVariants> & {
    asChild?: boolean;
  };

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { asChild, className, size, variant, ...props },
  ref,
) {
  const Component = asChild ? Slot : 'button';
  return <Component ref={ref} className={cn(buttonVariants({ size, variant }), className)} {...props} />;
});
