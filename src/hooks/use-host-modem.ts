"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { authFetch } from "@/lib/auth-fetch";

// vosclean: router modem mode (backend: pkg/api/zz_vosclean_hostmodem.go,
// daemon: vos5g-sdx55 vosclean/bin/hostmodem.sh).

const ENDPOINT = "/cgi-bin/quecmanager/system/host_modem.sh";
const KEY = ["host-modem"];

export interface HostModemStatus {
  success: boolean;
  /** vosclean on the stick has the router modem mode daemon */
  supported: boolean;
  /** HOST_MODEM=auto */
  enabled: boolean;
  /** A USB host has the data connection right now */
  active: boolean;
  /** Unix seconds the host took the connection (0 when not active) */
  since: number;
  /** USB composition running now, e.g. "9064" */
  usb_pid: string;
}

export function useHostModem() {
  const qc = useQueryClient();
  const query = useQuery<HostModemStatus>({
    queryKey: KEY,
    queryFn: async () => {
      const res = await authFetch(ENDPOINT);
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      return res.json();
    },
    refetchInterval: 15_000,
  });
  const save = useMutation({
    mutationFn: async (enabled: boolean) => {
      const res = await authFetch(ENDPOINT, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled }),
      });
      const data = await res.json();
      if (!data.success) throw new Error(data.detail || data.error || "Request failed");
      return data as HostModemStatus;
    },
    onSuccess: (d) => qc.setQueryData(KEY, d),
  });
  return { data: query.data ?? null, isLoading: query.isLoading, save };
}

/** USB compositions that offer a host QMI or MBIM (the stick can be a router's modem). */
export const MODEM_USB_PIDS = new Set(["9064"]);
