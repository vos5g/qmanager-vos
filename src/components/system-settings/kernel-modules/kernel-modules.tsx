"use client";

import { motion } from "motion/react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
  AlertTriangleIcon,
  CheckCircle2Icon,
  CircleDashedIcon,
  CpuIcon,
  Loader2Icon,
  MinusCircleIcon,
  RefreshCwIcon,
} from "lucide-react";

import { PageHeader } from "@/components/page-header";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { containerVariants, itemVariants } from "@/lib/motion";
import {
  useKernelModules,
  type KernelModuleGroup,
} from "@/hooks/use-kernel-modules";

function StateBadge({ group }: { group: KernelModuleGroup }) {
  const { t } = useTranslation("system-settings");
  if (group.state === "loaded") {
    return (
      <Badge variant="outline" className="border-success/30 bg-success/15 text-success">
        <CheckCircle2Icon className="size-3" />
        {t("kernel_modules.state_loaded")}
      </Badge>
    );
  }
  if (group.state === "partial") {
    return (
      <Badge variant="outline" className="border-warning/30 bg-warning/15 text-warning">
        <CircleDashedIcon className="size-3" />
        {t("kernel_modules.state_partial")}
      </Badge>
    );
  }
  return (
    <Badge
      variant="outline"
      className="border-muted-foreground/30 bg-muted/50 text-muted-foreground"
    >
      <MinusCircleIcon className="size-3" />
      {t("kernel_modules.state_unloaded")}
    </Badge>
  );
}

export default function KernelModules() {
  const { t } = useTranslation("system-settings");
  const { state, busyGroup, error, setGroup, reload } = useKernelModules();

  const toggle = async (g: KernelModuleGroup, next: boolean) => {
    const name = t(`kernel_modules.group.${g.id}.title`);
    const res = await setGroup(g.id, next);
    if (!res || !res.success) {
      toast.error(t("kernel_modules.toast_failed", { name }), {
        description: res?.detail,
      });
    } else if (res.warning === "still_in_use") {
      toast.warning(t("kernel_modules.toast_in_use", { name }));
    } else if (res.warning === "not_loaded") {
      toast.error(t("kernel_modules.toast_not_loaded", { name }));
    } else {
      toast.success(
        t(next ? "kernel_modules.toast_on" : "kernel_modules.toast_off", { name }),
      );
    }
  };

  return (
    <div className="@container/main mx-auto flex flex-col gap-6">
      <PageHeader
        icon={CpuIcon}
        title={t("kernel_modules.page_title")}
        description={t("kernel_modules.page_description")}
      />
      <motion.div
        className="grid grid-cols-1 gap-4"
        variants={containerVariants}
        initial="hidden"
        animate="visible"
      >
        <motion.div variants={itemVariants}>
          <Card className="@container/card">
            <CardHeader>
              <CardTitle>{t("kernel_modules.card_title")}</CardTitle>
              <CardDescription>
                {state
                  ? t("kernel_modules.card_description", {
                      kernel: state.kernel,
                      count: state.module_count,
                    })
                  : t("kernel_modules.card_description_loading")}
              </CardDescription>
              <CardAction>
                <Button
                  variant="ghost"
                  size="icon"
                  onClick={() => void reload()}
                  aria-label={t("kernel_modules.refresh")}
                >
                  <RefreshCwIcon className="size-4" />
                </Button>
              </CardAction>
            </CardHeader>
            <CardContent className="grid gap-4">
              {error && (
                <Alert variant="destructive">
                  <AlertTriangleIcon className="size-4" />
                  <AlertDescription>{error}</AlertDescription>
                </Alert>
              )}
              {state?.skipped_boot && (
                <Alert>
                  <AlertTriangleIcon className="size-4" />
                  <AlertDescription>{t("kernel_modules.skipped_boot")}</AlertDescription>
                </Alert>
              )}
              {state && !state.installed && (
                <Alert>
                  <AlertTriangleIcon className="size-4" />
                  <AlertDescription>{t("kernel_modules.not_installed")}</AlertDescription>
                </Alert>
              )}

              {!state ? (
                <div className="grid gap-3">
                  <Skeleton className="h-14 w-full" />
                  <Skeleton className="h-14 w-full" />
                  <Skeleton className="h-14 w-full" />
                </div>
              ) : (
                <div className="grid">
                  {state.groups.map((g, i) => (
                    <div key={g.id}>
                      {i > 0 && <Separator className="my-3" />}
                      <div className="flex items-start justify-between gap-4">
                        <Label htmlFor={`kmod-${g.id}`} className="grid gap-1 font-normal">
                          <span className="flex flex-wrap items-center gap-2 text-sm font-medium">
                            {t(`kernel_modules.group.${g.id}.title`)}
                            <StateBadge group={g} />
                          </span>
                          <span className="text-sm text-muted-foreground">
                            {t(`kernel_modules.group.${g.id}.description`)}
                          </span>
                          <span className="font-mono text-xs text-muted-foreground">
                            {g.modules.map((m) => m.name).join(" · ")}
                          </span>
                        </Label>
                        <div className="flex items-center gap-2 pt-0.5">
                          {busyGroup === g.id && (
                            <Loader2Icon className="size-4 animate-spin text-muted-foreground" />
                          )}
                          <Switch
                            id={`kmod-${g.id}`}
                            checked={g.enabled}
                            disabled={busyGroup !== null || (!state.installed && !g.enabled)}
                            onCheckedChange={(v) => void toggle(g, v)}
                          />
                        </div>
                      </div>
                    </div>
                  ))}
                </div>
              )}

              {state && (
                <p className="text-xs text-muted-foreground">
                  {t("kernel_modules.footer", { cc: state.tcp_congestion })}
                </p>
              )}
            </CardContent>
          </Card>
        </motion.div>

        {state?.log && state.log.length > 0 && (
          <motion.div variants={itemVariants}>
            <Card>
              <CardHeader>
                <CardTitle>{t("kernel_modules.log_title")}</CardTitle>
                <CardDescription>{t("kernel_modules.log_description")}</CardDescription>
              </CardHeader>
              <CardContent>
                <pre className="overflow-x-auto rounded-md border bg-muted/40 p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap">
                  {state.log.join("\n")}
                </pre>
              </CardContent>
            </Card>
          </motion.div>
        )}
      </motion.div>
    </div>
  );
}
