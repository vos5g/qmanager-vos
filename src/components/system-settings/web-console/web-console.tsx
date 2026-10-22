"use client";

import { motion } from "motion/react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { AlertTriangleIcon, SquareTerminalIcon } from "lucide-react";

import { PageHeader } from "@/components/page-header";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { containerVariants, itemVariants } from "@/lib/motion";
import { useWebConsoleSettings } from "@/hooks/use-web-console-settings";
import { ConsoleTerminal } from "./console-terminal";

export default function WebConsole() {
  const { t } = useTranslation("system-settings");
  const { enabled, saving, error, setConsoleEnabled } = useWebConsoleSettings();

  const toggle = async (next: boolean) => {
    const ok = await setConsoleEnabled(next);
    if (ok) toast.success(t(next ? "web_console.toast_on" : "web_console.toast_off"));
    else toast.error(t("web_console.toast_failed"));
  };

  return (
    <div className="@container/main mx-auto flex flex-col gap-6">
      <PageHeader
        icon={SquareTerminalIcon}
        title={t("web_console.page_title")}
        description={t("web_console.page_description")}
      />
      <motion.div
        className="grid grid-cols-1 gap-4"
        variants={containerVariants}
        initial="hidden"
        animate="visible"
      >
        <motion.div variants={itemVariants}>
          <Card>
            <CardHeader>
              <CardTitle>{t("web_console.settings_title")}</CardTitle>
              <CardDescription>{t("web_console.settings_description")}</CardDescription>
            </CardHeader>
            <CardContent className="grid gap-4">
              {enabled === null ? (
                <Skeleton className="h-6 w-48" />
              ) : (
                <div className="flex items-center justify-between gap-4">
                  <Label htmlFor="web-console-enabled" className="grid gap-1">
                    <span>{t("web_console.toggle_label")}</span>
                    <span className="text-sm font-normal text-muted-foreground">
                      {t("web_console.toggle_hint")}
                    </span>
                  </Label>
                  <Switch
                    id="web-console-enabled"
                    checked={enabled}
                    disabled={saving}
                    onCheckedChange={toggle}
                  />
                </div>
              )}
              {error && (
                <Alert variant="destructive">
                  <AlertTriangleIcon className="size-4" />
                  <AlertDescription>{error}</AlertDescription>
                </Alert>
              )}
            </CardContent>
          </Card>
        </motion.div>

        {enabled && (
          <motion.div variants={itemVariants}>
            <Card>
              <CardHeader>
                <CardTitle>{t("web_console.terminal_title")}</CardTitle>
                <CardDescription>{t("web_console.terminal_description")}</CardDescription>
              </CardHeader>
              <CardContent>
                <ConsoleTerminal />
              </CardContent>
            </Card>
          </motion.div>
        )}
      </motion.div>
    </div>
  );
}
