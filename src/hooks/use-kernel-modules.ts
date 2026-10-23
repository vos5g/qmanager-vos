"use client";

import { useCallback, useEffect, useState } from "react";
import { authFetch } from "@/lib/auth-fetch";

const ENDPOINT = "/cgi-bin/quecmanager/system/kernel_modules.sh";

export interface KernelModule {
  name: string;
  loaded: boolean;
  present: boolean;
}

export interface KernelModuleGroup {
  id: string;
  enabled: boolean;
  state: "loaded" | "partial" | "unloaded";
  modules: KernelModule[];
}

export interface KernelModulesState {
  success: boolean;
  kernel: string;
  installed: boolean;
  module_count: number;
  skipped_boot: boolean;
  tcp_congestion: string;
  groups: KernelModuleGroup[];
  log: string[] | null;
  warning?: "not_loaded" | "still_in_use";
  error?: string;
  detail?: string;
}

/** Reads the module groups and toggles them (vosclean loads/unloads). */
export function useKernelModules() {
  const [state, setState] = useState<KernelModulesState | null>(null);
  const [busyGroup, setBusyGroup] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const res = await authFetch(ENDPOINT);
      const data = (await res.json()) as KernelModulesState;
      if (!data.success) throw new Error(data.detail || data.error || "failed");
      setState(data);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  /** Returns the server's answer so the caller can word the toast. */
  const setGroup = useCallback(
    async (group: string, enabled: boolean): Promise<KernelModulesState | null> => {
      setBusyGroup(group);
      try {
        const res = await authFetch(ENDPOINT, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ group, enabled }),
        });
        const data = (await res.json()) as KernelModulesState;
        if (data.groups) setState(data);
        if (!data.success) setError(data.detail || data.error || "failed");
        else setError(null);
        return data;
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
        return null;
      } finally {
        setBusyGroup(null);
      }
    },
    [],
  );

  return { state, busyGroup, error, setGroup, reload: load };
}
