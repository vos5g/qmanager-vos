"use client";

import { useTranslation } from "react-i18next";
import {
  AlertTriangleIcon,
  CheckCircle2Icon,
  Loader2Icon,
  MinusCircleIcon,
  ShieldAlertIcon,
} from "lucide-react";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { formatBytes, type WgState, type WgStatus } from "@/hooks/use-wireguard";

function ago(s: number): string {
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
}

export function WireGuardPhaseBadge({ status }: { status: WgStatus }) {
  const { t } = useTranslation("monitoring");
  if (status.blocking) {
    return (
      <Badge variant="outline" className="border-destructive/30 bg-destructive/15 text-destructive">
        <ShieldAlertIcon className="size-3" />
        {t("wireguard.phase_blocking")}
      </Badge>
    );
  }
  switch (status.phase) {
    case "up":
      return (
        <Badge variant="outline" className="border-success/30 bg-success/15 text-success">
          <CheckCircle2Icon className="size-3" />
          {t("wireguard.phase_up")}
        </Badge>
      );
    case "starting":
    case "connecting":
      return (
        <Badge variant="outline" className="border-warning/30 bg-warning/15 text-warning">
          <Loader2Icon className="size-3 animate-spin" />
          {t("wireguard.phase_connecting")}
        </Badge>
      );
    case "error":
      return (
        <Badge variant="outline" className="border-destructive/30 bg-destructive/15 text-destructive">
          <AlertTriangleIcon className="size-3" />
          {t("wireguard.phase_error")}
        </Badge>
      );
    default:
      return (
        <Badge variant="outline" className="border-muted-foreground/30 bg-muted/50 text-muted-foreground">
          <MinusCircleIcon className="size-3" />
          {t("wireguard.phase_disconnected")}
        </Badge>
      );
  }
}

interface Props {
  state: WgState | null;
  busy: string | null;
  onDisconnect: () => void;
}

export function WireGuardStatusCard({ state, busy, onDisconnect }: Props) {
  const { t } = useTranslation("monitoring");
  const st = state?.status;
  const live = st?.live;

  const rows: [string, string][] = [];
  if (st?.active) {
    rows.push([t("wireguard.label_config"), st.active]);
    if (live?.endpoint) rows.push([t("wireguard.label_server"), live.endpoint]);
    rows.push([
      t("wireguard.label_handshake"),
      live?.handshake_age_s !== undefined
        ? t("wireguard.handshake_ago", { value: ago(live.handshake_age_s) })
        : t("wireguard.handshake_never"),
    ]);
    if (live) {
      rows.push([t("wireguard.label_received"), formatBytes(live.rx_bytes)]);
      rows.push([t("wireguard.label_sent"), formatBytes(live.tx_bytes)]);
    }
    if (st.engine) rows.push([t("wireguard.label_engine"), t(`wireguard.engine_${st.engine}`)]);
    if (st.route) rows.push([t("wireguard.label_routes"), t(`wireguard.route_${st.route}_long`)]);
    if (st.kill_switch !== undefined)
      rows.push([t("wireguard.label_kill_switch"), st.kill_switch ? t("wireguard.on") : t("wireguard.off")]);
  }

  return (
    <Card className="@container/card">
      <CardHeader>
        <CardTitle>{t("wireguard.status_title")}</CardTitle>
        <CardDescription>{t("wireguard.status_description")}</CardDescription>
        {st && (
          <CardAction>
            <WireGuardPhaseBadge status={st} />
          </CardAction>
        )}
      </CardHeader>
      <CardContent className="grid gap-4">
        {!st ? (
          <div className="grid gap-2">
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="h-5 w-full" />
            ))}
          </div>
        ) : !st.active ? (
          <p className="text-sm text-muted-foreground">{t("wireguard.status_not_connected")}</p>
        ) : (
          <>
            {st.blocking ? (
              <Alert variant="destructive">
                <ShieldAlertIcon className="size-4" />
                <AlertDescription>
                  <p>{t("wireguard.blocking_hint")}</p>
                  {st.error && <p className="mt-1 break-words opacity-90">{st.error}</p>}
                </AlertDescription>
              </Alert>
            ) : st.phase === "error" && st.error ? (
              <Alert variant="destructive">
                <AlertTriangleIcon className="size-4" />
                <AlertDescription>{st.error}</AlertDescription>
              </Alert>
            ) : st.phase === "connecting" ? (
              <p className="text-sm text-muted-foreground">{t("wireguard.connecting_hint")}</p>
            ) : null}
            <dl className="grid gap-x-6 gap-y-2 text-sm @2xl/card:grid-cols-2">
              {rows.map(([k, v]) => (
                <div key={k} className="flex items-baseline justify-between gap-4 border-b border-border/50 pb-2">
                  <dt className="text-muted-foreground">{k}</dt>
                  <dd className="min-w-0 truncate text-right font-medium tabular-nums" title={v}>{v}</dd>
                </div>
              ))}
            </dl>
            <div>
              <Button variant="outline" onClick={onDisconnect} disabled={busy !== null}>
                {busy === "disconnect" && <Loader2Icon className="size-4 animate-spin" />}
                {t("wireguard.disconnect")}
              </Button>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}
