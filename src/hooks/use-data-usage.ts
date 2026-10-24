"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { authFetch } from "@/lib/auth-fetch";

// vosclean: monthly mobile data usage, counted on the stick from the modem's
// per-call counters (backend: pkg/api/zz_vosclean_datausage.go).

const ENDPOINT = "/cgi-bin/quecmanager/monitoring/data_usage.sh";

export interface UsageTotals {
  rx: number;
  tx: number;
}

export interface DataUsage {
  success: boolean;
  /** False while the stick's clock isn't set yet (usage isn't booked then) */
  clock_ok: boolean;
  /** Day of the month the billing month starts (1-28) */
  reset_day: number;
  /** Monthly allowance in GB, 0 = none */
  cap_gb: number;
  /** Warn at this percentage of the allowance */
  warn_pct: number;
  period: UsageTotals & { start: string; end: string };
  months: (UsageTotals & { start: string })[];
  /** Last 30 days, oldest first */
  days: (UsageTotals & { date: string })[];
}

export interface DataUsageSettings {
  reset_day: number;
  cap_gb: number;
  warn_pct: number;
}

async function post(body: object): Promise<DataUsage> {
  const res = await authFetch(ENDPOINT, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const data = await res.json();
  if (!data.success) throw new Error(data.detail || data.error || "Request failed");
  return data;
}

export function useDataUsage() {
  const qc = useQueryClient();
  const query = useQuery<DataUsage>({
    queryKey: ["data-usage"],
    queryFn: async () => {
      const res = await authFetch(ENDPOINT);
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      return res.json();
    },
    refetchInterval: 60_000,
  });
  const save = useMutation({
    mutationFn: (s: DataUsageSettings) => post({ action: "save", ...s }),
    onSuccess: (d) => qc.setQueryData(["data-usage"], d),
  });
  const reset = useMutation({
    mutationFn: () => post({ action: "reset" }),
    onSuccess: (d) => qc.setQueryData(["data-usage"], d),
  });
  return { data: query.data ?? null, isLoading: query.isLoading, error: query.error, save, reset };
}

/** Decimal units, as carriers count allowances. */
export function formatUsage(bytes: number): string {
  if (bytes >= 1e12) return `${(bytes / 1e12).toFixed(2)} TB`;
  if (bytes >= 1e9) return `${(bytes / 1e9).toFixed(2)} GB`;
  if (bytes >= 1e6) return `${(bytes / 1e6).toFixed(1)} MB`;
  if (bytes >= 1e3) return `${Math.round(bytes / 1e3)} KB`;
  return `${bytes} B`;
}
