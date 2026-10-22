"use client";

import { useMemo } from "react";
import { motion } from "motion/react";
import { useTranslation } from "react-i18next";
import {
  DownloadIcon,
  Loader2Icon,
  PlayIcon,
  StethoscopeIcon,
  Trash2Icon,
  AlertTriangleIcon,
} from "lucide-react";

import { PageHeader } from "@/components/page-header";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Progress } from "@/components/ui/progress";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { containerVariants, itemVariants } from "@/lib/motion";
import { useSystemHealthCheck } from "@/hooks/use-system-health-check";
import type { HealthCheckTest, TestCategory } from "@/types/system-health-check";
import { HealthStatusBadge } from "./health-status-badge";
import { CheckRow } from "./check-row";

// Order the categories are shown in, and how many checks each holds
// (mirrors backend/pkg/api/zz_vosclean_health.go). Used for progress only.
const CATEGORIES: { id: TestCategory; count: number }[] = [
  { id: "binaries", count: 3 },
  { id: "permissions", count: 5 },
  { id: "at_transport", count: 5 },
  { id: "sms", count: 2 },
  { id: "sudoers", count: 5 },
  { id: "services", count: 5 },
  { id: "network", count: 5 },
  { id: "configuration", count: 4 },
];
const TOTAL = CATEGORIES.reduce((n, c) => n + c.count, 0);

function formatBytes(n: number) {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

export default function SystemHealthCheck() {
  const { t, i18n } = useTranslation("system-settings");
  const hc = useSystemHealthCheck();
  const { job } = hc;

  const byCategory = useMemo(() => {
    const m = new Map<TestCategory, HealthCheckTest[]>();
    for (const test of job?.tests ?? []) {
      const list = m.get(test.category) ?? [];
      list.push(test);
      m.set(test.category, list);
    }
    return m;
  }, [job]);

  const attention = (job?.tests ?? []).filter(
    (x) => x.status === "fail" || x.status === "warn",
  );
  const finished = (job?.tests ?? []).filter(
    (x) => x.status !== "pending" && x.status !== "running",
  ).length;
  const summary = job?.summary;
  const busy = hc.isRunning || hc.isStarting;

  const lastRun =
    job?.finished_at != null
      ? new Date(job.finished_at * 1000).toLocaleString(i18n.language)
      : null;

  return (
    <div className="@container/main mx-auto flex flex-col gap-6">
      <PageHeader
        icon={StethoscopeIcon}
        title={t("health_check.page_title")}
        description={t("health_check.page_description")}
      />

      <motion.div
        className="grid grid-cols-1 gap-4 @3xl/main:grid-cols-2"
        variants={containerVariants}
        initial="hidden"
        animate="visible"
      >
        {/* Run + summary */}
        <motion.div variants={itemVariants}>
          <Card className="@container/card h-full">
            <CardHeader>
              <CardTitle>{t("health_check.run_title")}</CardTitle>
              <CardDescription>{t("health_check.run_description")}</CardDescription>
            </CardHeader>
            <CardContent className="grid gap-4">
              {hc.isLoading ? (
                <Skeleton className="h-16 w-full" />
              ) : hc.isRunning ? (
                <div className="grid gap-2">
                  <div className="flex items-center justify-between text-sm">
                    <span className="text-muted-foreground">
                      {t("health_check.running")}
                    </span>
                    <span className="tabular-nums text-muted-foreground">
                      {finished} / {TOTAL}
                    </span>
                  </div>
                  <Progress value={(finished / TOTAL) * 100} />
                </div>
              ) : summary && job ? (
                <div className="grid gap-3">
                  <div className="flex flex-wrap gap-2">
                    <HealthStatusBadge status="pass" count={summary.pass} />
                    <HealthStatusBadge status="warn" count={summary.warn} />
                    <HealthStatusBadge status="fail" count={summary.fail} />
                    {summary.skip > 0 && (
                      <HealthStatusBadge status="skip" count={summary.skip} />
                    )}
                  </div>
                  {lastRun && (
                    <p className="text-sm text-muted-foreground">
                      {t("health_check.last_run", {
                        when: lastRun,
                        seconds: ((summary.duration_ms ?? 0) / 1000).toFixed(1),
                      })}
                    </p>
                  )}
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">
                  {t("health_check.never_run")}
                </p>
              )}

              {hc.error && !hc.isLoading && (
                <Alert variant="destructive">
                  <AlertTriangleIcon className="size-4" />
                  <AlertDescription>{hc.error}</AlertDescription>
                </Alert>
              )}
              {job?.error && (
                <Alert variant="destructive">
                  <AlertTriangleIcon className="size-4" />
                  <AlertDescription>{job.error}</AlertDescription>
                </Alert>
              )}

              <div className="flex flex-col gap-2 @sm/card:flex-row @sm/card:flex-wrap">
                <Button onClick={hc.start} disabled={busy || hc.isLoading}>
                  {busy ? (
                    <Loader2Icon className="size-4 animate-spin" />
                  ) : (
                    <PlayIcon className="size-4" />
                  )}
                  {busy
                    ? t("health_check.button_running")
                    : job
                      ? t("health_check.button_rerun")
                      : t("health_check.button_run")}
                </Button>
                {job?.tarball_path && !hc.isRunning && (
                  <Button
                    variant="outline"
                    onClick={hc.downloadBundle}
                    disabled={hc.isDownloading}
                  >
                    {hc.isDownloading ? (
                      <Loader2Icon className="size-4 animate-spin" />
                    ) : (
                      <DownloadIcon className="size-4" />
                    )}
                    {t("health_check.button_download")}
                    {job.tarball_size ? (
                      <span className="tabular-nums text-muted-foreground">
                        {formatBytes(job.tarball_size)}
                      </span>
                    ) : null}
                  </Button>
                )}
                {job && !hc.isRunning && (
                  <Button
                    variant="ghost"
                    onClick={hc.clear}
                    disabled={hc.isClearing}
                  >
                    <Trash2Icon className="size-4" />
                    {t("health_check.button_clear")}
                  </Button>
                )}
              </div>
              {job?.tarball_path && !hc.isRunning && (
                <p className="text-xs text-muted-foreground">
                  {t("health_check.bundle_note")}
                </p>
              )}
            </CardContent>
          </Card>
        </motion.div>

        {/* Needs attention */}
        <motion.div variants={itemVariants}>
          <Card className="h-full">
            <CardHeader>
              <CardTitle>{t("health_check.attention_title")}</CardTitle>
              <CardDescription>
                {t("health_check.attention_description")}
              </CardDescription>
            </CardHeader>
            <CardContent>
              {hc.isLoading ? (
                <Skeleton className="h-16 w-full" />
              ) : !job || hc.isRunning ? (
                <p className="text-sm text-muted-foreground">
                  {hc.isRunning
                    ? t("health_check.attention_wait")
                    : t("health_check.attention_none_yet")}
                </p>
              ) : attention.length === 0 ? (
                <p className="text-sm text-muted-foreground">
                  {t("health_check.attention_all_good")}
                </p>
              ) : (
                <div className="-mx-2 grid">
                  {[...attention]
                    .sort((a, b) => (a.status === "fail" ? -1 : 0) - (b.status === "fail" ? -1 : 0))
                    .map((test) => (
                      <CheckRow
                        key={test.id}
                        test={test}
                        fetchOutput={hc.fetchTestOutput}
                      />
                    ))}
                </div>
              )}
            </CardContent>
          </Card>
        </motion.div>

        {/* All checks by category */}
        {job && job.tests.length > 0 && (
          <motion.div variants={itemVariants} className="@3xl/main:col-span-2">
            <Card>
              <CardHeader>
                <CardTitle>{t("health_check.all_title")}</CardTitle>
                <CardDescription>{t("health_check.all_description")}</CardDescription>
              </CardHeader>
              <CardContent className="grid gap-5">
                {CATEGORIES.filter((c) => byCategory.has(c.id)).map((c, i) => (
                  <section key={c.id} className="grid gap-1">
                    {i > 0 && <Separator className="mb-4" />}
                    <h3 className="text-sm font-semibold">
                      {t(`health_check.category.${c.id}.title`)}
                    </h3>
                    <p className="mb-1 text-sm text-muted-foreground">
                      {t(`health_check.category.${c.id}.description`)}
                    </p>
                    <div className="-mx-2 grid">
                      {byCategory.get(c.id)!.map((test) => (
                        <CheckRow
                          key={test.id}
                          test={test}
                          fetchOutput={hc.fetchTestOutput}
                        />
                      ))}
                    </div>
                  </section>
                ))}
              </CardContent>
            </Card>
          </motion.div>
        )}
      </motion.div>
    </div>
  );
}
