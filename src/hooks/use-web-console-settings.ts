"use client";

import { useCallback, useEffect, useState } from "react";
import { authFetch } from "@/lib/auth-fetch";

const ENDPOINT = "/cgi-bin/quecmanager/system/web_console.sh";

interface ConsoleSettingsResponse {
  success: boolean;
  enabled?: boolean;
  active?: number;
  error?: string;
  detail?: string;
}

/** Reads and toggles whether the web console is allowed (off by default). */
export function useWebConsoleSettings() {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [active, setActive] = useState(0);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const res = await authFetch(ENDPOINT);
      const data = (await res.json()) as ConsoleSettingsResponse;
      if (!data.success) throw new Error(data.detail || data.error || "failed");
      setEnabled(!!data.enabled);
      setActive(data.active ?? 0);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setEnabled(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const setConsoleEnabled = useCallback(async (next: boolean) => {
    setSaving(true);
    setError(null);
    try {
      const res = await authFetch(ENDPOINT, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: next }),
      });
      const data = (await res.json()) as ConsoleSettingsResponse;
      if (!data.success) throw new Error(data.detail || data.error || "failed");
      setEnabled(!!data.enabled);
      setActive(data.active ?? 0);
      return true;
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      return false;
    } finally {
      setSaving(false);
    }
  }, []);

  return { enabled, active, saving, error, setConsoleEnabled, reload: load };
}
