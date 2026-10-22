"use client";

import { useTranslation } from "react-i18next";
import {
  CheckCircle2Icon,
  CircleDashedIcon,
  Loader2Icon,
  MinusCircleIcon,
  TriangleAlertIcon,
  XCircleIcon,
  type LucideIcon,
} from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import type { TestStatus } from "@/types/system-health-check";

// Status badge per DESIGN.md: outline + 15% tint + 30% border + size-3 icon.
const STATUS_STYLE: Record<TestStatus, { cls: string; icon: LucideIcon; spin?: boolean }> = {
  pass: { cls: "border-success/30 bg-success/15 text-success", icon: CheckCircle2Icon },
  warn: { cls: "border-warning/30 bg-warning/15 text-warning", icon: TriangleAlertIcon },
  fail: { cls: "border-destructive/30 bg-destructive/15 text-destructive", icon: XCircleIcon },
  skip: { cls: "border-muted-foreground/30 bg-muted/50 text-muted-foreground", icon: MinusCircleIcon },
  pending: { cls: "border-muted-foreground/30 bg-muted/50 text-muted-foreground", icon: CircleDashedIcon },
  running: { cls: "border-info/30 bg-info/15 text-info", icon: Loader2Icon, spin: true },
};

export function HealthStatusBadge({
  status,
  count,
  className,
}: {
  status: TestStatus;
  /** When set, the badge reads "3 passed" instead of "Passed". */
  count?: number;
  className?: string;
}) {
  const { t } = useTranslation("system-settings");
  const s = STATUS_STYLE[status];
  const Icon = s.icon;
  return (
    <Badge variant="outline" className={cn(s.cls, "tabular-nums", className)}>
      <Icon className={cn("size-3", s.spin && "animate-spin")} />
      {count === undefined
        ? t(`health_check.status.${status}`)
        : t(`health_check.count.${status}`, { count })}
    </Badge>
  );
}
