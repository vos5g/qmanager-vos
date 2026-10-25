"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { authFetch } from "@/lib/auth-fetch";

// vosclean: QManager updates from GitHub releases (backend:
// pkg/api/zz_vosclean_update.go).

const ENDPOINT = "/cgi-bin/quecmanager/system/update.sh";
const KEY = ["qm-update"];

export interface QmRelease {
  tag: string;
  version: string;
  title: string;
  /** Release notes (Markdown, may be cut short) */
  notes: string;
  published_at: string;
  prerelease: boolean;
  size: number;
  url: string;
  current: boolean;
  /** Has this updater (v1.2.0+); older ones can't update themselves */
  has_updater: boolean;
}

export interface QmUpdateStatus {
  success: boolean;
  current_version: string;
  /** False for a local (development) build */
  release_build: boolean;
  latest_version: string;
  update_available: boolean;
  previous_version: string;
  can_rollback: boolean;
  /** A just-installed version is still being checked (rollback guard armed) */
  verifying: boolean;
  /** Set when a version didn't start and the previous one was put back */
  rolled_back_from: string;
  releases: QmRelease[];
  progress: {
    state: "idle" | "downloading" | "installing" | "restarting" | "error";
    target: string;
    done: number;
    total: number;
    message: string;
  };
  error: string;
  checked_at: number;
  repo: string;
}

async function post(body: object): Promise<QmUpdateStatus> {
  const res = await authFetch(ENDPOINT, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const data = await res.json();
  if (!data.success) throw new Error(data.detail || data.error || "Request failed");
  return data;
}

export function useQmUpdate({ fast = false }: { fast?: boolean } = {}) {
  const qc = useQueryClient();
  const query = useQuery<QmUpdateStatus>({
    queryKey: KEY,
    queryFn: async () => {
      const res = await authFetch(ENDPOINT);
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      return res.json();
    },
    // While installing (and while QManager restarts) poll quickly; the request
    // fails for a few seconds during the restart, which is expected.
    refetchInterval: (q) => {
      const s = q.state.data?.progress.state;
      return fast || s === "downloading" || s === "installing" || s === "restarting" ? 2_000 : 60_000;
    },
    retry: fast ? true : 1,
    retryDelay: 2_000,
  });
  const onSuccess = (d: QmUpdateStatus) => qc.setQueryData(KEY, d);
  const check = useMutation({ mutationFn: () => post({ action: "check" }), onSuccess });
  const install = useMutation({ mutationFn: (tag: string) => post({ action: "install", tag }), onSuccess });
  const rollback = useMutation({ mutationFn: () => post({ action: "rollback" }), onSuccess });
  return {
    data: query.data ?? null,
    isLoading: query.isLoading,
    isError: query.isError,
    check,
    install,
    rollback,
  };
}

export function formatMB(bytes: number) {
  return `${(bytes / 1e6).toFixed(1)} MB`;
}
