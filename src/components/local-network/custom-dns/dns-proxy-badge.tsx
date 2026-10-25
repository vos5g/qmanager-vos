"use client";

import { useTranslation } from "react-i18next";
import { AlertTriangleIcon, CheckCircle2Icon, MinusCircleIcon } from "lucide-react";
import { Badge } from "@/components/ui/badge";

export type DnsProxyBadgeState = "active" | "warning" | "off";

/** Outline status badge (DESIGN.md status-badge pattern). */
export function DnsProxyBadge({ state, label }: { state: DnsProxyBadgeState; label?: string }) {
  const { t } = useTranslation("local-network");
  if (state === "active") {
    return (
      <Badge variant="outline" className="border-success/30 bg-success/15 text-success">
        <CheckCircle2Icon className="size-3" />
        {label ?? t("shared.status_active")}
      </Badge>
    );
  }
  if (state === "warning") {
    return (
      <Badge variant="outline" className="border-warning/30 bg-warning/15 text-warning">
        <AlertTriangleIcon className="size-3" />
        {label}
      </Badge>
    );
  }
  return (
    <Badge variant="outline" className="border-muted-foreground/30 bg-muted/50 text-muted-foreground">
      <MinusCircleIcon className="size-3" />
      {label ?? t("shared.status_inactive")}
    </Badge>
  );
}

/** A label/value row used in the status blocks of both cards. */
export function StatRow({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-4 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="text-right font-medium tabular-nums">{value}</span>
    </div>
  );
}
