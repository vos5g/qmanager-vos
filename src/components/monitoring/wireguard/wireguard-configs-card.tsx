"use client";

import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
  AlertTriangleIcon,
  CopyIcon,
  FileKeyIcon,
  Loader2Icon,
  MoreHorizontalIcon,
  PencilIcon,
  PlusIcon,
  Trash2Icon,
} from "lucide-react";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import type { WgActionResult, WgConfigSummary, WgState } from "@/hooks/use-wireguard";
import { copyText } from "./wireguard-config-dialog";

interface Props {
  state: WgState | null;
  busy: string | null;
  onAdd: () => void;
  onEdit: (name: string) => void;
  connect: (name: string) => Promise<WgActionResult>;
  disconnect: () => Promise<WgActionResult>;
  deleteConfig: (name: string) => Promise<WgActionResult>;
}

function OptionPill({ children }: { children: React.ReactNode }) {
  return (
    <Badge variant="outline" className="border-border bg-muted/40 font-normal text-muted-foreground">
      {children}
    </Badge>
  );
}

export function WireGuardConfigsCard({ state, busy, onAdd, onEdit, connect, disconnect, deleteConfig }: Props) {
  const { t } = useTranslation("monitoring");
  const [toDelete, setToDelete] = useState<string | null>(null);
  const active = state?.status.active ?? null;
  const blockedByNetBird = state?.netbird_running ?? false;

  const run = async (p: Promise<WgActionResult>, ok: string) => {
    const res = await p;
    if (res.success) toast.success(ok);
    else toast.error(t("wireguard.toast_failed"), { description: res.detail || res.error });
  };

  const summary = (c: WgConfigSummary) => {
    const parts: string[] = [];
    if (c.endpoint) parts.push(c.endpoint);
    if (c.addresses?.length) parts.push(c.addresses.join(", "));
    parts.push(
      c.full_tunnel
        ? t("wireguard.all_traffic")
        : t("wireguard.split_ranges", { count: c.allowed_ips?.length ?? 0 }),
    );
    return parts.join(" · ");
  };

  return (
    <Card className="@container/card">
      <CardHeader>
        <CardTitle>{t("wireguard.configs_title")}</CardTitle>
        <CardDescription>{t("wireguard.configs_description")}</CardDescription>
        {state && state.configs.length > 0 && (
          <CardAction>
            <Button size="sm" onClick={onAdd}>
              <PlusIcon className="size-4" />
              {t("wireguard.add_config")}
            </Button>
          </CardAction>
        )}
      </CardHeader>
      <CardContent className="grid gap-4">
        {blockedByNetBird && !active && (
          <Alert>
            <AlertTriangleIcon className="size-4" />
            <AlertDescription>{t("wireguard.netbird_running")}</AlertDescription>
          </Alert>
        )}
        {!state ? (
          <div className="grid gap-3">
            <Skeleton className="h-14 w-full" />
            <Skeleton className="h-14 w-full" />
          </div>
        ) : state.configs.length === 0 ? (
          <Empty className="bg-muted/30">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <FileKeyIcon />
              </EmptyMedia>
              <EmptyTitle>{t("wireguard.empty_title")}</EmptyTitle>
              <EmptyDescription className="max-w-sm text-pretty">
                {t("wireguard.empty_description")}
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button onClick={onAdd}>
                <PlusIcon className="size-4" />
                {t("wireguard.add_config")}
              </Button>
            </EmptyContent>
          </Empty>
        ) : (
          <ul className="grid">
            {state.configs.map((c, i) => {
              const isActive = c.name === active;
              const rowBusy = busy === `connect:${c.name}` || busy === `delete:${c.name}` || (isActive && busy === "disconnect");
              return (
                <li key={c.name}>
                  {i > 0 && <Separator className="my-3" />}
                  <div className="flex flex-col gap-3 @md/card:flex-row @md/card:items-center @md/card:justify-between">
                    <div className="grid min-w-0 gap-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="truncate text-sm font-medium">{c.name}</span>
                        {isActive && (
                          <Badge variant="outline" className="border-primary/30 bg-primary/15 text-primary">
                            {t("wireguard.active_badge")}
                          </Badge>
                        )}
                      </div>
                      {c.error ? (
                        <span className="text-sm text-destructive">
                          {t("wireguard.broken_config", { error: c.error })}
                        </span>
                      ) : (
                        <>
                          <span className="truncate text-sm text-muted-foreground tabular-nums">{summary(c)}</span>
                          <div className="flex flex-wrap gap-1.5 pt-0.5">
                            <OptionPill>{t(`wireguard.engine_${c.engine}`)}</OptionPill>
                            <OptionPill>{t(`wireguard.route_${c.route}`)}</OptionPill>
                            {c.kill_switch && <OptionPill>{t("wireguard.kill_switch_badge")}</OptionPill>}
                          </div>
                        </>
                      )}
                    </div>
                    <div className="flex shrink-0 items-center gap-2">
                      {isActive ? (
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={busy !== null}
                          onClick={() => void run(disconnect(), t("wireguard.toast_disconnected"))}
                        >
                          {rowBusy && <Loader2Icon className="size-4 animate-spin" />}
                          {t("wireguard.disconnect")}
                        </Button>
                      ) : (
                        <Button
                          variant="secondary"
                          size="sm"
                          disabled={busy !== null || !!c.error || blockedByNetBird}
                          onClick={() => void run(connect(c.name), t("wireguard.toast_connected", { name: c.name }))}
                        >
                          {rowBusy && <Loader2Icon className="size-4 animate-spin" />}
                          {t("wireguard.connect")}
                        </Button>
                      )}
                      <DropdownMenu>
                        <DropdownMenuTrigger asChild>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="size-8"
                            aria-label={t("wireguard.menu_label", { name: c.name })}
                            disabled={busy !== null}
                          >
                            <MoreHorizontalIcon className="size-4" />
                          </Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end">
                          <DropdownMenuItem onSelect={() => onEdit(c.name)} disabled={!!c.error}>
                            <PencilIcon className="size-4" />
                            {t("wireguard.edit")}
                          </DropdownMenuItem>
                          {c.public_key && (
                            <DropdownMenuItem
                              onSelect={() =>
                                void copyText(c.public_key!).then(() => toast.success(t("wireguard.toast_copied")))
                              }
                            >
                              <CopyIcon className="size-4" />
                              {t("wireguard.copy_public_key")}
                            </DropdownMenuItem>
                          )}
                          <DropdownMenuSeparator />
                          <DropdownMenuItem variant="destructive" onSelect={() => setToDelete(c.name)}>
                            <Trash2Icon className="size-4" />
                            {t("wireguard.delete")}
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </div>
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </CardContent>

      <AlertDialog open={toDelete !== null} onOpenChange={(o) => !o && setToDelete(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("wireguard.delete_title", { name: toDelete ?? "" })}</AlertDialogTitle>
            <AlertDialogDescription>
              {toDelete === active ? t("wireguard.delete_description_active") : t("wireguard.delete_description")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("wireguard.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={() => {
                const n = toDelete!;
                setToDelete(null);
                void run(deleteConfig(n), t("wireguard.toast_deleted", { name: n }));
              }}
            >
              {t("wireguard.delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  );
}
