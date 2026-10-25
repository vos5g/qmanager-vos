"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { authFetch } from "@/lib/auth-fetch";

// vosclean: the stick's LED (backend: pkg/api/zz_vosclean_led.go).

const ENDPOINT = "/cgi-bin/quecmanager/system/led.sh";
const KEY = ["status-light"];

export type StatusLightMode = "on" | "off" | "schedule";

export interface StatusLightSettings {
  mode: StatusLightMode;
  /** "HH:MM" in `timezone` */
  off_from: string;
  off_to: string;
  /** IANA zone of the times; the browser's zone is sent on save */
  timezone?: string;
}

export interface StatusLight extends StatusLightSettings {
  success: boolean;
  /** QManager has the light switched off right now */
  light_off: boolean;
  error: string;
  /** Current time "HH:MM" in `timezone`, so schedules make sense */
  device_time: string;
  zone_abbr: string;
  clock_ok: boolean;
}

export function useStatusLight() {
  const qc = useQueryClient();
  const query = useQuery<StatusLight>({
    queryKey: KEY,
    queryFn: async () => {
      const res = await authFetch(ENDPOINT);
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      return res.json();
    },
    refetchInterval: 30_000,
  });
  const save = useMutation({
    mutationFn: async (s: StatusLightSettings) => {
      const res = await authFetch(ENDPOINT, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ action: "save", timezone: browserTimeZone(), ...s }),
      });
      const data = await res.json();
      if (!data.success) throw new Error(data.detail || data.error || "Request failed");
      return data as StatusLight;
    },
    onSuccess: (d) => qc.setQueryData(KEY, d),
  });
  return { data: query.data ?? null, isLoading: query.isLoading, save };
}

function browserTimeZone(): string | undefined {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || undefined;
  } catch {
    return undefined;
  }
}
