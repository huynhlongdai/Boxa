import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

const badgeVariants = cva("inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[11.5px] font-medium leading-4 whitespace-nowrap", {
  variants: {
    variant: {
      default: "border-border bg-surface-2 text-foreground",
      outline: "border-border text-muted-foreground",
      green: "border-st-running/30 bg-st-running/10 text-st-running",
      gray: "border-st-sleeping/30 bg-st-sleeping/10 text-st-sleeping",
      blue: "border-st-provisioning/30 bg-st-provisioning/10 text-st-provisioning",
      yellow: "border-st-warning/30 bg-st-warning/10 text-st-warning",
      red: "border-st-error/30 bg-st-error/10 text-st-error",
    },
  },
  defaultVariants: { variant: "default" },
});

export interface BadgeProps extends React.HTMLAttributes<HTMLSpanElement>, VariantProps<typeof badgeVariants> {}

export function Badge({ className, variant, ...props }: BadgeProps) {
  return <span className={cn(badgeVariants({ variant }), className)} {...props} />;
}
