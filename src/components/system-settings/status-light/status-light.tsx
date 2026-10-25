"use client";

import { useState } from "react";
import { motion } from "motion/react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { CheckCircle2Icon, LightbulbIcon, MinusCircleIcon, MoonIcon } from "lucide-react";

import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { SaveButton, useSaveFlash } from "@/components/ui/save-button";
import { Skeleton } from "@/components/ui/skeleton";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { containerVariants, itemVariants } from "@/lib/motion";
import {
  useStatusLight,
  type StatusLight as StatusLightData,
  type StatusLightMode,
  type StatusLightSettings,
} from "@/hooks/use-status-light";

function LightBadge({ data }: { data: StatusLightData }) {
  const { t } = useTranslation("system-settings");
  if (data.light_off) {
    const Icon = data.mode === "schedule" ? MoonIcon : MinusCircleIcon;
    return (
      <Badge variant="outline" className="border-muted-foreground/30 bg-muted/50 text-muted-foreground">
        <Icon className="size-3" />
        {data.mode === "schedule" ? t("status_light.badge_night") : t("status_light.badge_off")}
      </Badge>
    );
  }
  return (
    <Badge variant="outline" className="border-success/30 bg-success/15 text-success">
      <CheckCircle2Icon className="size-3" />
      {t("status_light.badge_on")}
    </Badge>
  );
}

function LightForm({ data }: { data: StatusLightData }) {
  const { t } = useTranslation("system-settings");
  const { save } = useStatusLight();
  const { saved, markSaved } = useSaveFlash();
  const [form, setForm] = useState<StatusLightSettings>({
    mode: data.mode,
    off_from: data.off_from,
    off_to: data.off_to,
  });
  const set = (p: Partial<StatusLightSettings>) => setForm((f) => ({ ...f, ...p }));
  const sameTimes = form.mode === "schedule" && form.off_from === form.off_to;
  const dirty =
    form.mode !== data.mode ||
    (form.mode === "schedule" && (form.off_from !== data.off_from || form.off_to !== data.off_to));

  const onSave = () =>
    save.mutate(form, {
      onSuccess: () => {
        markSaved();
        toast.success(t("status_light.toast_saved"));
      },
      onError: (e) => toast.error(e instanceof Error ? e.message : t("status_light.toast_error")),
    });

  return (
    <form
      className="grid gap-5"
      onSubmit={(e) => {
        e.preventDefault();
        onSave();
      }}
    >
      <FieldGroup>
        <Field>
          <FieldLabel>{t("status_light.mode")}</FieldLabel>
          <ToggleGroup
            type="single"
            variant="outline"
            value={form.mode}
            onValueChange={(v) => v && set({ mode: v as StatusLightMode })}
            aria-label={t("status_light.mode")}
          >
            <ToggleGroupItem value="on" className="px-4">
              {t("status_light.mode_on")}
            </ToggleGroupItem>
            <ToggleGroupItem value="schedule" className="px-4">
              {t("status_light.mode_schedule")}
            </ToggleGroupItem>
            <ToggleGroupItem value="off" className="px-4">
              {t("status_light.mode_off")}
            </ToggleGroupItem>
          </ToggleGroup>
          <FieldDescription>{t(`status_light.mode_${form.mode}_hint`)}</FieldDescription>
        </Field>

        {form.mode === "schedule" && (
          <div className="grid gap-4 @md/card:grid-cols-2">
            <Field>
              <FieldLabel htmlFor="led-from">{t("status_light.off_from")}</FieldLabel>
              <Input
                id="led-from"
                type="time"
                className="tabular-nums"
                value={form.off_from}
                onChange={(e) => set({ off_from: e.target.value })}
                aria-invalid={sameTimes || undefined}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="led-to">{t("status_light.off_to")}</FieldLabel>
              <Input
                id="led-to"
                type="time"
                className="tabular-nums"
                value={form.off_to}
                onChange={(e) => set({ off_to: e.target.value })}
                aria-invalid={sameTimes || undefined}
              />
            </Field>
            {sameTimes && <FieldError className="@md/card:col-span-2">{t("status_light.same_times")}</FieldError>}
            <p className="text-xs text-muted-foreground @md/card:col-span-2 tabular-nums">
              {data.clock_ok
                ? t("status_light.device_time", {
                    time: data.device_time,
                    zone: data.timezone || data.zone_abbr,
                  })
                : t("status_light.clock_wait")}
            </p>
          </div>
        )}
      </FieldGroup>

      {data.error && <p className="text-sm text-destructive break-words">{data.error}</p>}

      <p className="text-xs text-muted-foreground">{t("status_light.note")}</p>

      <SaveButton
        type="submit"
        isSaving={save.isPending}
        saved={saved}
        label={t("status_light.save")}
        className="w-fit"
        disabled={!dirty || sameTimes}
      />
    </form>
  );
}

export default function StatusLight() {
  const { t } = useTranslation("system-settings");
  const { data, isLoading } = useStatusLight();
  return (
    <div className="@container/main mx-auto flex flex-col gap-6">
      <PageHeader
        icon={LightbulbIcon}
        title={t("status_light.page_title")}
        description={t("status_light.page_description")}
      />
      <motion.div
        className="grid grid-cols-1 gap-4 @3xl/main:grid-cols-2"
        variants={containerVariants}
        initial="hidden"
        animate="visible"
      >
        <motion.div variants={itemVariants}>
          <Card className="@container/card">
            <CardHeader>
              <CardTitle>{t("status_light.card_title")}</CardTitle>
              <CardDescription>{t("status_light.card_description")}</CardDescription>
              {data && (
                <CardAction>
                  <LightBadge data={data} />
                </CardAction>
              )}
            </CardHeader>
            <CardContent>
              {isLoading || !data ? (
                <div className="grid gap-4">
                  <Skeleton className="h-9 w-64" />
                  <Skeleton className="h-4 w-full" />
                  <Skeleton className="h-9 w-24" />
                </div>
              ) : (
                <LightForm key={`${data.mode}|${data.off_from}|${data.off_to}`} data={data} />
              )}
            </CardContent>
          </Card>
        </motion.div>
      </motion.div>
    </div>
  );
}
