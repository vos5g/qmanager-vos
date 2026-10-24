"use client";

import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ArrowDownIcon, ArrowUpIcon, Settings2Icon } from "lucide-react";
import { toast } from "sonner";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
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
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { SaveButton, useSaveFlash } from "@/components/ui/save-button";
import { Skeleton } from "@/components/ui/skeleton";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { formatUsage, useDataUsage, type DataUsage } from "@/hooks/use-data-usage";

function fmtDate(iso: string, lang: string) {
  const d = new Date(`${iso}T00:00:00`);
  return d.toLocaleDateString(lang, { day: "numeric", month: "short" });
}

function DailyBars({ days, lang }: { days: DataUsage["days"]; lang: string }) {
  const { t } = useTranslation("dashboard");
  const max = Math.max(1, ...days.map((d) => d.rx + d.tx));
  return (
    <div className="grid gap-1.5">
      <div className="flex h-16 items-end gap-[3px]" role="img" aria-label={t("data_usage.daily_aria")}>
        {days.map((d) => {
          const total = d.rx + d.tx;
          return (
            <Tooltip key={d.date}>
              <TooltipTrigger asChild>
                <div className="flex h-full flex-1 items-end">
                  <div
                    className={cn("w-full rounded-sm", total > 0 ? "bg-primary/70" : "bg-muted")}
                    style={{ height: `${total > 0 ? Math.max(4, (total / max) * 100) : 4}%` }}
                  />
                </div>
              </TooltipTrigger>
              <TooltipContent>
                <p className="tabular-nums">
                  {fmtDate(d.date, lang)}: {formatUsage(total)}
                </p>
              </TooltipContent>
            </Tooltip>
          );
        })}
      </div>
      <div className="flex justify-between text-[10px] text-muted-foreground">
        <span>{days.length ? fmtDate(days[0].date, lang) : ""}</span>
        <span>{t("data_usage.today")}</span>
      </div>
    </div>
  );
}

function SettingsDialog({
  open,
  onOpenChange,
  data,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  data: DataUsage;
}) {
  const { t } = useTranslation("dashboard");
  const { save, reset } = useDataUsage();
  const { saved, markSaved } = useSaveFlash();
  const [resetDay, setResetDay] = useState(String(data.reset_day));
  const [cap, setCap] = useState(data.cap_gb ? String(data.cap_gb) : "");
  const [warn, setWarn] = useState(String(data.warn_pct));

  const day = Number(resetDay);
  const capN = cap.trim() === "" ? 0 : Number(cap);
  const warnN = Number(warn);
  const dayErr = !Number.isInteger(day) || day < 1 || day > 28;
  const capErr = !Number.isFinite(capN) || capN < 0 || capN > 100000;
  const warnErr = !Number.isInteger(warnN) || warnN < 50 || warnN > 100;

  const onSave = () => {
    save.mutate(
      { reset_day: day, cap_gb: capN, warn_pct: warnN },
      {
        onSuccess: () => {
          markSaved();
          setTimeout(() => onOpenChange(false), 600);
        },
        onError: (e) => toast.error(e instanceof Error ? e.message : t("data_usage.save_error")),
      },
    );
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("data_usage.settings_title")}</DialogTitle>
          <DialogDescription>{t("data_usage.settings_description")}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-5">
          <Field>
            <FieldLabel htmlFor="du-reset-day">{t("data_usage.reset_day")}</FieldLabel>
            <Input
              id="du-reset-day"
              type="number"
              inputMode="numeric"
              min={1}
              max={28}
              className="tabular-nums"
              value={resetDay}
              onChange={(e) => setResetDay(e.target.value)}
              aria-invalid={dayErr}
            />
            <FieldDescription>{t("data_usage.reset_day_description")}</FieldDescription>
          </Field>
          <Field>
            <FieldLabel htmlFor="du-cap">{t("data_usage.cap")}</FieldLabel>
            <Input
              id="du-cap"
              type="number"
              inputMode="decimal"
              min={0}
              step="any"
              placeholder={t("data_usage.cap_placeholder")}
              className="tabular-nums"
              value={cap}
              onChange={(e) => setCap(e.target.value)}
              aria-invalid={capErr}
            />
            <FieldDescription>{t("data_usage.cap_description")}</FieldDescription>
          </Field>
          {capN > 0 && (
            <Field>
              <FieldLabel htmlFor="du-warn">{t("data_usage.warn")}</FieldLabel>
              <Input
                id="du-warn"
                type="number"
                inputMode="numeric"
                min={50}
                max={100}
                className="tabular-nums"
                value={warn}
                onChange={(e) => setWarn(e.target.value)}
                aria-invalid={warnErr}
              />
              <FieldDescription>{t("data_usage.warn_description")}</FieldDescription>
            </Field>
          )}
        </div>
        <DialogFooter className="gap-2 sm:justify-between">
          <AlertDialog>
            <AlertDialogTrigger asChild>
              <Button variant="outline" disabled={reset.isPending}>
                {t("data_usage.reset_count")}
              </Button>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>{t("data_usage.reset_title")}</AlertDialogTitle>
                <AlertDialogDescription>{t("data_usage.reset_description")}</AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>{t("data_usage.cancel")}</AlertDialogCancel>
                <AlertDialogAction
                  onClick={() =>
                    reset.mutate(undefined, {
                      onSuccess: () => toast.success(t("data_usage.reset_done")),
                      onError: () => toast.error(t("data_usage.save_error")),
                    })
                  }
                >
                  {t("data_usage.reset_confirm")}
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
          <SaveButton
            isSaving={save.isPending}
            saved={saved}
            onClick={onSave}
            disabled={dayErr || capErr || (capN > 0 && warnErr)}
            label={t("data_usage.save")}
          />
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function DataUsageCard() {
  const { t, i18n } = useTranslation("dashboard");
  const { data, isLoading } = useDataUsage();
  const [open, setOpen] = useState(false);

  const total = data ? data.period.rx + data.period.tx : 0;
  const capBytes = data && data.cap_gb > 0 ? data.cap_gb * 1e9 : 0;
  const pct = capBytes ? Math.min(100, (total / capBytes) * 100) : 0;
  const barColor =
    pct >= 100 ? "bg-destructive" : data && pct >= data.warn_pct ? "bg-warning" : "bg-primary";

  return (
    <Card className="@container/card">
      <CardHeader>
        <CardTitle>{t("data_usage.title")}</CardTitle>
        <CardDescription>
          {data
            ? t("data_usage.period", {
                start: fmtDate(data.period.start, i18n.language),
                end: fmtDate(data.period.end, i18n.language),
              })
            : t("data_usage.description")}
        </CardDescription>
        {data && (
          <CardAction>
            <Button variant="ghost" size="icon" aria-label={t("data_usage.settings_aria")} onClick={() => setOpen(true)}>
              <Settings2Icon className="size-4" />
            </Button>
          </CardAction>
        )}
      </CardHeader>
      <CardContent className="grid gap-4">
        {isLoading || !data ? (
          <div className="grid gap-3">
            <Skeleton className="h-8 w-32" />
            <Skeleton className="h-4 w-full" />
            <Skeleton className="h-16 w-full" />
          </div>
        ) : (
          <>
            <div className="flex flex-wrap items-end justify-between gap-x-6 gap-y-2">
              <div>
                <p className="text-3xl font-semibold tracking-tight tabular-nums">{formatUsage(total)}</p>
                {capBytes > 0 && (
                  <p className="text-sm text-muted-foreground tabular-nums">
                    {t("data_usage.of_cap", { cap: formatUsage(capBytes), pct: Math.round(pct) })}
                  </p>
                )}
              </div>
              <div className="flex gap-4 text-sm tabular-nums">
                <span className="inline-flex items-center gap-1">
                  <ArrowDownIcon className="size-3.5 text-muted-foreground" aria-hidden />
                  <span className="sr-only">{t("data_usage.download")}</span>
                  {formatUsage(data.period.rx)}
                </span>
                <span className="inline-flex items-center gap-1">
                  <ArrowUpIcon className="size-3.5 text-muted-foreground" aria-hidden />
                  <span className="sr-only">{t("data_usage.upload")}</span>
                  {formatUsage(data.period.tx)}
                </span>
              </div>
            </div>
            {capBytes > 0 && (
              <div
                className="h-2 w-full overflow-hidden rounded-full bg-muted"
                role="progressbar"
                aria-valuenow={Math.round(pct)}
                aria-valuemin={0}
                aria-valuemax={100}
              >
                <div className={cn("h-full rounded-full transition-all", barColor)} style={{ width: `${pct}%` }} />
              </div>
            )}
            <DailyBars days={data.days} lang={i18n.language} />
            {!data.clock_ok && (
              <p className="text-xs text-muted-foreground">{t("data_usage.clock_wait")}</p>
            )}
            <p className="text-xs text-muted-foreground">{t("data_usage.note")}</p>
          </>
        )}
      </CardContent>
      {data && open && <SettingsDialog open={open} onOpenChange={setOpen} data={data} />}
    </Card>
  );
}
