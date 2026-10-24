"use client";

import { useTranslation } from "react-i18next";
import { SignalStatusCard } from "./signal-status-card";
import type { LteStatus, NrAvailability } from "@/types/modem-status";
import {
  RSRP_THRESHOLDS,
  RSRQ_THRESHOLDS,
  SINR_THRESHOLDS,
} from "@/types/modem-status";
import { getDLFrequency, formatFrequency } from "@/lib/earfcn";

interface LTEStatusComponentProps {
  data: LteStatus | null;
  isLoading: boolean;
  /** vosclean: whether the serving LTE cell offers 5G (AT+5GDISP?) */
  nrAvailability?: NrAvailability | null;
  /** Current network type ("LTE", "5G-NSA", ...) */
  networkType?: string;
}

const LTEStatusComponent = ({ data, isLoading, nrAvailability, networkType }: LTEStatusComponentProps) => {
  const { t } = useTranslation("dashboard");

  const fmt = (value: number | null | undefined, unit: string) => {
    if (value === null || value === undefined) return "-";
    return `${value} ${unit}`;
  };

  const rows = [
    { label: t("signal_status.band"), value: data?.band || "-" },
    { label: t("signal_status.earfcn"), value: data?.earfcn?.toString() ?? "-" },
    {
      label: t("signal_status.frequency"),
      value:
        data?.earfcn != null
          ? formatFrequency(getDLFrequency(data.earfcn, "LTE"))
          : "-",
    },
    { label: t("signal_status.pci"), value: data?.pci?.toString() ?? "-" },
    {
      label: t("signal_status.rsrp"),
      value: fmt(data?.rsrp, "dBm"),
      rawValue: data?.rsrp,
      thresholds: RSRP_THRESHOLDS,
    },
    {
      label: t("signal_status.rsrq"),
      value: fmt(data?.rsrq, "dB"),
      rawValue: data?.rsrq,
      thresholds: RSRQ_THRESHOLDS,
    },
    { label: t("signal_status.rssi"), value: fmt(data?.rssi, "dBm") },
    {
      label: t("signal_status.sinr"),
      value: fmt(data?.sinr, "dB"),
      rawValue: data?.sinr,
      thresholds: SINR_THRESHOLDS,
    },
    // vosclean (AT+BINFO): what the link is doing, which explains speed
    // better than signal strength alone
    ...(data?.ul_modulation || data?.tx_power != null
      ? [{
          label: t("signal_status.uplink"),
          value: [data?.ul_modulation, data?.tx_power != null ? `${data.tx_power} dBm` : ""]
            .filter(Boolean)
            .join(" · ") || "-",
        }]
      : []),
    ...(data?.dl_modulation || data?.dl_layers
      ? [{
          label: t("signal_status.downlink"),
          value: [
            data?.dl_modulation,
            data?.dl_layers
              ? t("signal_status.layers", { n: data.dl_layers, of: data.dl_conf_layers || data.dl_layers })
              : "",
          ]
            .filter(Boolean)
            .join(" · ") || "-",
        }]
      : []),
    ...(data?.cqi != null
      ? [{ label: t("signal_status.cqi"), value: `${data.cqi} / 15` }]
      : []),
    // vosclean: does this cell offer 5G? (answers "why am I on LTE only?")
    ...(nrAvailability
      ? [{
          label: t("signal_status.nr_on_cell"),
          value: t(`signal_status.nr_on_cell_${nrOnCell(nrAvailability, networkType)}`),
        }]
      : []),
  ];

  return (
    <SignalStatusCard
      title={t("signal_status.lte_primary_title")}
      state={data?.state ?? "unknown"}
      rsrp={data?.rsrp ?? null}
      rows={rows}
      isLoading={isLoading}
    />
  );
};

/** Why 5G is or isn't available on the serving LTE cell. */
export function nrOnCell(na: NrAvailability, networkType?: string): string {
  if (networkType === "5G-NSA") return "in_use";
  if (!na.endc_enabled) return "off_on_device";
  if (na.restrict_dcnr) return "blocked";
  return na.upper_layer_ind ? "offered" : "not_offered";
}

export default LTEStatusComponent;
