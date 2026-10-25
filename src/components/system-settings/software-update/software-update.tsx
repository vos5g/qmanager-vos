"use client";

import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { motion } from "motion/react";
import { toast } from "sonner";
import {
  AlertTriangleIcon,
  CheckCircle2Icon,
  ChevronDownIcon,
  DownloadCloudIcon,
  DownloadIcon,
  ExternalLinkIcon,
  FlaskConicalIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  TriangleAlertIcon,
} from "lucide-react";

import { PageHeader } from "@/components/page-header";
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
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Progress } from "@/components/ui/progress";
import { Skeleton } from "@/components/ui/skeleton";
import { containerVariants, itemVariants } from "@/lib/motion";
import { cn } from "@/lib/utils";
import { formatMB, useQmUpdate, type QmRelease, type QmUpdateStatus } from "@/hooks/use-qm-update";

// =============================================================================
// Software Update — QManager releases from GitHub (vos5g/qmanager-vos)
// =============================================================================
// Install the latest release, any older one (downgrade) or go back to the
// version that ran before. The stick checks each download against the
// release's SHA256SUMS.txt, and puts the previous binary back by itself if a
// new one doesn't start.
// =============================================================================

function StateBadge({ data }: { data: QmUpdateStatus }) {
  const { t } = useTranslation("system-settings");
  const p = data.progress.state;
  if (p === "downloading" || p === "installing" || p === "restarting") {
    return (
      <Badge variant="outline" className="border-info/30 bg-info/15 text-info">
        <DownloadIcon className="size-3" />
        {t("vos_update.badge_installing")}
      </Badge>
    );
  }
  if (!data.release_build) {
    return (
      <Badge variant="outline" className="border-muted-foreground/30 bg-muted/50 text-muted-foreground">
        <FlaskConicalIcon className="size-3" />
        {t("vos_update.badge_dev")}
      </Badge>
    );
  }
  if (data.update_available) {
    return (
      <Badge variant="outline" className="border-warning/30 bg-warning/15 text-warning">
        <TriangleAlertIcon className="size-3" />
        {t("vos_update.badge_available")}
      </Badge>
    );
  }
  return (
    <Badge variant="outline" className="border-success/30 bg-success/15 text-success">
      <CheckCircle2Icon className="size-3" />
      {t("vos_update.badge_current")}
    </Badge>
  );
}

function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-4 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="text-right font-medium tabular-nums">{value}</span>
    </div>
  );
}

function fmtDate(iso: string, lang: string) {
  if (!iso) return "";
  return new Date(iso).toLocaleDateString(lang, { day: "numeric", month: "short", year: "numeric" });
}

function Notes({ release }: { release: QmRelease }) {
  const { t } = useTranslation("system-settings");
  const [open, setOpen] = useState(false);
  if (!release.notes.trim()) return null;
  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <CollapsibleTrigger asChild>
        <Button type="button" variant="ghost" size="sm" className="-ml-2 w-fit text-muted-foreground">
          <ChevronDownIcon className={cn("size-4 transition-transform", open && "rotate-180")} />
          {t("vos_update.release_notes")}
        </Button>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="mt-2 max-h-72 overflow-y-auto rounded-md border bg-muted/30 p-3 text-xs leading-relaxed whitespace-pre-wrap break-words">
          {release.notes}
        </div>
        {release.url && (
          <a
            href={release.url}
            target="_blank"
            rel="noreferrer"
            className="mt-2 inline-flex items-center gap-1 text-xs text-primary hover:underline"
          >
            {t("vos_update.on_github")}
            <ExternalLinkIcon className="size-3" />
          </a>
        )}
      </CollapsibleContent>
    </Collapsible>
  );
}

/** Confirmation for installing a release (update, reinstall or downgrade). */
function InstallDialog({
  release,
  current,
  onConfirm,
  onClose,
}: {
  release: QmRelease | null;
  current: string;
  onConfirm: (r: QmRelease) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation("system-settings");
  if (!release) return null;
  const older = release.version.localeCompare(current, undefined, { numeric: true }) < 0;
  return (
    <AlertDialog open onOpenChange={(o) => !o && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {older
              ? t("vos_update.confirm_downgrade_title", { version: release.version })
              : t("vos_update.confirm_install_title", { version: release.version })}
          </AlertDialogTitle>
          <AlertDialogDescription asChild>
            <div className="grid gap-2">
              <p>{t("vos_update.confirm_body", { size: formatMB(release.size) })}</p>
              {!release.has_updater && (
                <p className="text-warning">{t("vos_update.confirm_no_updater", { version: release.version })}</p>
              )}
            </div>
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{t("actions.cancel", { ns: "common" })}</AlertDialogCancel>
          <AlertDialogAction onClick={() => onConfirm(release)}>
            {older ? t("vos_update.downgrade") : t("vos_update.install")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

export default function SoftwareUpdate() {
  const { t, i18n } = useTranslation("system-settings");
  const [awaiting, setAwaiting] = useState<string | null>(null);
  const { data, isLoading, isError, check, install, rollback } = useQmUpdate({ fast: awaiting !== null });
  const [confirm, setConfirm] = useState<QmRelease | null>(null);
  const [confirmRollback, setConfirmRollback] = useState(false);
  const [done, setDone] = useState<string | null>(null);

  // Once QManager is back on the version we waited for, stop waiting
  // (render-phase adjustment, so no extra effect pass).
  const [prevData, setPrevData] = useState<QmUpdateStatus | null>(null);
  if (data && data !== prevData) {
    setPrevData(data);
    if (awaiting && data.current_version === awaiting && data.progress.state === "idle") {
      setDone(awaiting);
      setAwaiting(null);
    } else if (awaiting && data.progress.state === "error") {
      setAwaiting(null);
    }
  }
  useEffect(() => {
    if (done) toast.success(t("vos_update.toast_done", { version: done }));
  }, [done, t]);

  const onError = (e: unknown) => toast.error(e instanceof Error ? e.message : t("vos_update.toast_error"));
  const doInstall = (r: QmRelease) => {
    setConfirm(null);
    setDone(null);
    install.mutate(r.tag, { onSuccess: () => setAwaiting(r.version), onError });
  };

  const p = data?.progress;
  const busy = !!p && (p.state === "downloading" || p.state === "installing" || p.state === "restarting");
  const restarting = p?.state === "restarting" || (awaiting !== null && (isError || data?.current_version !== awaiting));
  const latest = data?.releases.find((r) => r.version === data.latest_version) ?? null;
  const pct = p && p.total > 0 ? Math.round((p.done / p.total) * 100) : 0;

  return (
    <div className="@container/main mx-auto flex flex-col gap-6">
      <PageHeader icon={DownloadCloudIcon} title={t("vos_update.page_title")} description={t("vos_update.page_description")} />
      <motion.div
        className="grid grid-cols-1 items-start gap-4 @3xl/main:grid-cols-2"
        variants={containerVariants}
        initial="hidden"
        animate="visible"
      >
        {/* ── Current version and the latest release ─────────────────────── */}
        <motion.div variants={itemVariants}>
          <Card className="@container/card">
            <CardHeader>
              <CardTitle>{t("vos_update.card_title")}</CardTitle>
              <CardDescription>{t("vos_update.card_description")}</CardDescription>
              {data && (
                <CardAction>
                  <StateBadge data={data} />
                </CardAction>
              )}
            </CardHeader>
            <CardContent className="grid gap-4">
              {isLoading || !data ? (
                <div className="grid gap-3">
                  <Skeleton className="h-5 w-full" />
                  <Skeleton className="h-5 w-full" />
                  <Skeleton className="h-9 w-32" />
                </div>
              ) : (
                <>
                  <div className="grid gap-2">
                    <Row
                      label={t("vos_update.installed")}
                      value={data.release_build ? data.current_version : t("vos_update.dev_build", { v: data.current_version })}
                    />
                    <Row label={t("vos_update.latest")} value={data.latest_version || "–"} />
                    {data.checked_at > 0 && (
                      <Row
                        label={t("vos_update.checked")}
                        value={new Date(data.checked_at * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}
                      />
                    )}
                  </div>

                  {data.rolled_back_from && (
                    <div className="flex gap-2 rounded-lg border border-warning/30 bg-warning/10 p-3 text-sm">
                      <AlertTriangleIcon className="mt-0.5 size-4 shrink-0 text-warning" />
                      <p>{t("vos_update.rolled_back", { version: data.rolled_back_from })}</p>
                    </div>
                  )}
                  {data.error && <p className="text-sm text-destructive break-words">{data.error}</p>}
                  {p?.state === "error" && p.message && (
                    <p className="text-sm text-destructive break-words">
                      {t("vos_update.failed", { version: p.target, error: p.message })}
                    </p>
                  )}

                  {busy || restarting ? (
                    <div className="grid gap-2 rounded-lg border bg-muted/30 p-4" aria-live="polite">
                      <p className="text-sm font-medium">
                        {p?.state === "downloading"
                          ? t("vos_update.downloading", { version: p.target })
                          : p?.state === "installing"
                            ? t("vos_update.installing", { version: p.target })
                            : t("vos_update.restarting", { version: awaiting ?? p?.target })}
                      </p>
                      {p?.state === "downloading" && p.total > 0 && (
                        <>
                          <Progress value={pct} aria-label={t("vos_update.downloading", { version: p.target })} />
                          <p className="text-xs tabular-nums text-muted-foreground">
                            {formatMB(p.done)} / {formatMB(p.total)}
                          </p>
                        </>
                      )}
                      {restarting && <p className="text-xs text-muted-foreground">{t("vos_update.restarting_hint")}</p>}
                    </div>
                  ) : data.verifying ? (
                    <p className="text-sm text-muted-foreground">
                      {t("vos_update.verifying", { version: data.current_version })}
                    </p>
                  ) : null}

                  {data.update_available && latest && !busy && !restarting && (
                    <div className="grid gap-2 rounded-lg border p-4">
                      <div className="flex flex-wrap items-baseline justify-between gap-2">
                        <p className="text-sm font-medium">{latest.title}</p>
                        <span className="text-xs text-muted-foreground">{fmtDate(latest.published_at, i18n.language)}</span>
                      </div>
                      <Notes release={latest} />
                      <Button className="w-fit" onClick={() => setConfirm(latest)}>
                        <DownloadIcon className="size-4" />
                        {t("vos_update.install_version", { version: latest.version })}
                      </Button>
                    </div>
                  )}

                  <div className="flex flex-wrap gap-2">
                    <Button variant="outline" onClick={() => check.mutate(undefined, { onError })} disabled={check.isPending || busy}>
                      <RefreshCwIcon className={cn("size-4", check.isPending && "animate-spin")} />
                      {t("vos_update.check_now")}
                    </Button>
                    {data.can_rollback && (
                      <Button variant="outline" onClick={() => setConfirmRollback(true)} disabled={busy || rollback.isPending}>
                        <RotateCcwIcon className="size-4" />
                        {data.previous_version
                          ? t("vos_update.rollback_to", { version: data.previous_version })
                          : t("vos_update.rollback")}
                      </Button>
                    )}
                  </div>
                  <p className="text-xs text-muted-foreground">{t("vos_update.safety_note")}</p>
                </>
              )}
            </CardContent>
          </Card>
        </motion.div>

        {/* ── All releases: reinstall or downgrade ───────────────────────── */}
        <motion.div variants={itemVariants}>
          <Card className="@container/card">
            <CardHeader>
              <CardTitle>{t("vos_update.versions_title")}</CardTitle>
              <CardDescription>{t("vos_update.versions_description")}</CardDescription>
            </CardHeader>
            <CardContent>
              {isLoading || !data ? (
                <div className="grid gap-3">
                  <Skeleton className="h-12 w-full" />
                  <Skeleton className="h-12 w-full" />
                </div>
              ) : data.releases.length === 0 ? (
                <p className="text-sm text-muted-foreground">{t("vos_update.no_releases")}</p>
              ) : (
                <ul className="divide-y">
                  {data.releases.map((r) => (
                    <li key={r.tag} className="flex flex-wrap items-center justify-between gap-3 py-3 first:pt-0 last:pb-0">
                      <div className="grid gap-0.5">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="text-sm font-medium tabular-nums">{r.version}</span>
                          {r.current && (
                            <Badge variant="outline" className="border-success/30 bg-success/15 text-success">
                              <CheckCircle2Icon className="size-3" />
                              {t("vos_update.tag_installed")}
                            </Badge>
                          )}
                          {r.version === data.latest_version && !r.current && (
                            <Badge variant="outline" className="border-primary/30 bg-primary/10 text-primary">
                              {t("vos_update.tag_latest")}
                            </Badge>
                          )}
                          {r.prerelease && (
                            <Badge variant="outline" className="border-warning/30 bg-warning/15 text-warning">
                              {t("vos_update.tag_prerelease")}
                            </Badge>
                          )}
                        </div>
                        <span className="text-xs text-muted-foreground">
                          {fmtDate(r.published_at, i18n.language)} · {formatMB(r.size)}
                        </span>
                      </div>
                      <Button
                        variant="outline"
                        size="sm"
                        disabled={r.current || busy || restarting}
                        onClick={() => setConfirm(r)}
                      >
                        {r.version.localeCompare(data.current_version, undefined, { numeric: true }) < 0 && data.release_build
                          ? t("vos_update.downgrade")
                          : t("vos_update.install")}
                      </Button>
                    </li>
                  ))}
                </ul>
              )}
            </CardContent>
          </Card>
        </motion.div>
      </motion.div>

      <InstallDialog
        release={confirm}
        current={data?.current_version ?? ""}
        onConfirm={doInstall}
        onClose={() => setConfirm(null)}
      />
      <AlertDialog open={confirmRollback} onOpenChange={setConfirmRollback}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {data?.previous_version
                ? t("vos_update.rollback_to", { version: data.previous_version })
                : t("vos_update.rollback")}
            </AlertDialogTitle>
            <AlertDialogDescription>{t("vos_update.rollback_body")}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("actions.cancel", { ns: "common" })}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                const target = data?.previous_version || null;
                setDone(null);
                rollback.mutate(undefined, {
                  onSuccess: () => setAwaiting(target),
                  onError,
                });
              }}
            >
              {t("vos_update.rollback")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
