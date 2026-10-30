"use client";

import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { CheckCircle2Icon, MinusCircleIcon, RouterIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Field, FieldContent, FieldDescription, FieldLabel } from "@/components/ui/field";
import { SaveButton, useSaveFlash } from "@/components/ui/save-button";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { MODEM_USB_PIDS, useHostModem, type HostModemStatus } from "@/hooks/use-host-modem";

// vosclean: router modem mode. While a USB host (e.g. OpenWrt on 9064 QMI)
// uses the stick as its modem, the stick releases its own data call so the
// host can connect on the same APN.

function HostModemBadge({ data }: { data: HostModemStatus }) {
  const { t } = useTranslation("system-settings");
  if (!data.enabled) {
    return (
      <Badge variant="outline" className="border-muted-foreground/30 bg-muted/50 text-muted-foreground">
        <MinusCircleIcon className="size-3" />
        {t("host_modem.badge_off")}
      </Badge>
    );
  }
  if (data.active) {
    return (
      <Badge variant="outline" className="border-info/30 bg-info/15 text-info">
        <RouterIcon className="size-3" />
        {t("host_modem.badge_host")}
      </Badge>
    );
  }
  return (
    <Badge variant="outline" className="border-success/30 bg-success/15 text-success">
      <CheckCircle2Icon className="size-3" />
      {t("host_modem.badge_stick")}
    </Badge>
  );
}

function HostModemForm({ data }: { data: HostModemStatus }) {
  const { t } = useTranslation("system-settings");
  const { save } = useHostModem();
  const { saved, markSaved } = useSaveFlash();
  const [enabled, setEnabled] = useState(data.enabled);

  const onSave = () =>
    save.mutate(enabled, {
      onSuccess: () => {
        markSaved();
        toast.success(enabled ? t("host_modem.toast_on") : t("host_modem.toast_off"));
      },
      onError: (e) => toast.error(e instanceof Error ? e.message : t("host_modem.toast_error")),
    });

  return (
    <form
      className="grid gap-5"
      onSubmit={(e) => {
        e.preventDefault();
        onSave();
      }}
    >
      <Field orientation="horizontal" data-disabled={!data.supported || undefined}>
        <FieldContent>
          <FieldLabel htmlFor="host-modem-auto">{t("host_modem.switch")}</FieldLabel>
          <FieldDescription>{t("host_modem.switch_hint")}</FieldDescription>
        </FieldContent>
        <Switch
          id="host-modem-auto"
          checked={enabled}
          onCheckedChange={setEnabled}
          disabled={!data.supported}
        />
      </Field>

      {!data.supported && <p className="text-sm text-destructive">{t("host_modem.unsupported")}</p>}
      {data.active && data.since > 0 && (
        <p className="text-sm tabular-nums">
          {t("host_modem.since", { time: new Date(data.since * 1000).toLocaleString() })}
        </p>
      )}
      <p className="text-xs text-muted-foreground">{t("host_modem.while_host")}</p>
      {data.supported && !MODEM_USB_PIDS.has(data.usb_pid) && (
        <p className="text-xs text-muted-foreground">{t("host_modem.needs_9064")}</p>
      )}

      <SaveButton
        type="submit"
        isSaving={save.isPending}
        saved={saved}
        label={t("host_modem.save")}
        className="w-fit"
        disabled={!data.supported || enabled === data.enabled}
      />
    </form>
  );
}

export function HostModemCard() {
  const { t } = useTranslation("system-settings");
  const { data, isLoading } = useHostModem();
  return (
    <Card className="@container/card">
      <CardHeader>
        <CardTitle>{t("host_modem.card_title")}</CardTitle>
        <CardDescription>{t("host_modem.card_description")}</CardDescription>
        {data && (
          <CardAction>
            <HostModemBadge data={data} />
          </CardAction>
        )}
      </CardHeader>
      <CardContent>
        {isLoading || !data ? (
          <div className="grid gap-4">
            <Skeleton className="h-9 w-full" />
            <Skeleton className="h-4 w-3/4" />
            <Skeleton className="h-9 w-24" />
          </div>
        ) : (
          <HostModemForm key={String(data.enabled)} data={data} />
        )}
      </CardContent>
    </Card>
  );
}
