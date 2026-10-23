"use client";

import { useTranslation } from "react-i18next";

import {
  Field,
  FieldContent,
  FieldDescription,
  FieldLabel,
  FieldSet,
  FieldLegend,
  FieldTitle,
} from "@/components/ui/field";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Switch } from "@/components/ui/switch";
import type { WgOptions } from "@/hooks/use-wireguard";

interface WireGuardOptionsProps {
  value: WgOptions;
  onChange: (next: WgOptions) => void;
  kernelAvailable: boolean;
  idPrefix: string;
  disabled?: boolean;
}

/** Engine, routing scope and kill switch: the stick's own options for a tunnel. */
export function WireGuardOptions({
  value,
  onChange,
  kernelAvailable,
  idPrefix,
  disabled,
}: WireGuardOptionsProps) {
  const { t } = useTranslation("monitoring");
  const id = (s: string) => `${idPrefix}-${s}`;

  return (
    <div className="grid gap-5">
      <FieldSet>
        <FieldLegend variant="label">{t("wireguard.engine_label")}</FieldLegend>
        <RadioGroup
          value={value.engine}
          onValueChange={(v) => onChange({ ...value, engine: v as WgOptions["engine"] })}
          className="grid gap-2 @md/dialog:grid-cols-2"
          disabled={disabled}
        >
          <FieldLabel htmlFor={id("engine-kernel")}>
            <Field orientation="horizontal" data-disabled={!kernelAvailable || undefined}>
              <FieldContent>
                <FieldTitle>{t("wireguard.engine_kernel")}</FieldTitle>
                <FieldDescription>{t("wireguard.engine_kernel_desc")}</FieldDescription>
              </FieldContent>
              <RadioGroupItem
                value="kernel"
                id={id("engine-kernel")}
                disabled={!kernelAvailable && value.engine !== "kernel"}
              />
            </Field>
          </FieldLabel>
          <FieldLabel htmlFor={id("engine-userspace")}>
            <Field orientation="horizontal">
              <FieldContent>
                <FieldTitle>{t("wireguard.engine_userspace")}</FieldTitle>
                <FieldDescription>{t("wireguard.engine_userspace_desc")}</FieldDescription>
              </FieldContent>
              <RadioGroupItem value="userspace" id={id("engine-userspace")} />
            </Field>
          </FieldLabel>
        </RadioGroup>
        {!kernelAvailable && (
          <FieldDescription>{t("wireguard.engine_kernel_missing")}</FieldDescription>
        )}
      </FieldSet>

      <FieldSet>
        <FieldLegend variant="label">{t("wireguard.route_label")}</FieldLegend>
        <RadioGroup
          value={value.route}
          onValueChange={(v) => onChange({ ...value, route: v as WgOptions["route"] })}
          className="grid gap-2 @md/dialog:grid-cols-2"
          disabled={disabled}
        >
          <FieldLabel htmlFor={id("route-lan")}>
            <Field orientation="horizontal">
              <FieldContent>
                <FieldTitle>{t("wireguard.route_lan_long")}</FieldTitle>
                <FieldDescription>{t("wireguard.route_lan_desc")}</FieldDescription>
              </FieldContent>
              <RadioGroupItem value="lan" id={id("route-lan")} />
            </Field>
          </FieldLabel>
          <FieldLabel htmlFor={id("route-all")}>
            <Field orientation="horizontal">
              <FieldContent>
                <FieldTitle>{t("wireguard.route_all_long")}</FieldTitle>
                <FieldDescription>{t("wireguard.route_all_desc")}</FieldDescription>
              </FieldContent>
              <RadioGroupItem value="all" id={id("route-all")} />
            </Field>
          </FieldLabel>
        </RadioGroup>
      </FieldSet>

      <Field orientation="horizontal" className="justify-between">
        <FieldContent>
          <FieldLabel htmlFor={id("kill-switch")}>{t("wireguard.kill_switch_label")}</FieldLabel>
          <FieldDescription>{t("wireguard.kill_switch_desc")}</FieldDescription>
        </FieldContent>
        <Switch
          id={id("kill-switch")}
          checked={value.kill_switch}
          onCheckedChange={(v) => onChange({ ...value, kill_switch: v })}
          disabled={disabled}
        />
      </Field>
    </div>
  );
}
