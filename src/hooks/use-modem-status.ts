"use client";

import { useQuery } from "@tanstack/react-query";
import { authFetch } from "@/lib/auth-fetch";
import { useLiveInterval } from "@/components/realtime-provider";
import type { ModemStatus } from "@/types/modem-status";

// =============================================================================
// useModemStatus — Polling Hook for QManager Dashboard
// =============================================================================
// Fetches the cached modem status JSON from the CGI endpoint at a regular
// interval using TanStack Query. Provides loading/error states and staleness
// detection.
//
// Usage:
//   const { data, isLoading, isStale, error, refresh } = useModemStatus();
//
// The hook does NOT touch the modem — it only reads the pre-built JSON cache.
// =============================================================================

/** How often to poll the CGI endpoint (ms) */
const DEFAULT_POLL_INTERVAL = 2000;

/** After this many seconds without a fresh timestamp, data is "stale" */
const STALE_THRESHOLD_SECONDS = 10;

/** CGI endpoint path (proxied in dev via next.config.ts rewrites) */
const FETCH_ENDPOINT = "/cgi-bin/quecmanager/at_cmd/fetch_data.sh";

// Shared by every useModemStatus instance: the last status timestamp seen and
// when (browser time) it first appeared.
let lastStamp: number | null = null;
let lastStampSeenAt = 0;

/** Seconds since the status timestamp last changed, on the browser's clock. */
export function statusAgeSeconds(stamp: number, now = Date.now()): number {
  if (stamp !== lastStamp) {
    lastStamp = stamp;
    lastStampSeenAt = now;
  }
  return Math.floor((now - lastStampSeenAt) / 1000);
}

export interface UseModemStatusOptions {
  /** Polling interval in ms (default: 2000) */
  pollInterval?: number;
  /** Whether polling is active (default: true) */
  enabled?: boolean;
}

export interface UseModemStatusReturn {
  /** The latest modem status data (null before first successful fetch) */
  data: ModemStatus | null;
  /** True during the very first fetch (before any data is available) */
  isLoading: boolean;
  /** True if the data's timestamp is older than the stale threshold */
  isStale: boolean;
  /** Error message if the last fetch failed */
  error: string | null;
  /** Manually trigger an immediate refresh */
  refresh: () => void;
}

export function useModemStatus(
  options: UseModemStatusOptions = {}
): UseModemStatusReturn {
  const { pollInterval = DEFAULT_POLL_INTERVAL, enabled = true } = options;

  const liveInterval = useLiveInterval(pollInterval);

  const query = useQuery<ModemStatus>({
    queryKey: ["modem-status"],
    queryFn: async () => {
      const response = await authFetch(FETCH_ENDPOINT);
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }
      return response.json();
    },
    refetchInterval: enabled ? liveInterval : false,
    enabled,
  });

  // TanStack Query already keeps previous data on refetch (placeholderData
  // via keepPreviousData-like behavior is default), so data stays present.
  const data = query.data ?? null;

  // Staleness (vosclean): measured on the browser's clock, from when the
  // status timestamp last changed. Comparing the timestamp itself with the
  // browser's clock showed OFFLINE whenever the stick's clock was off (it has
  // no battery-backed clock and boots at 1970 until NITZ or NTP sets it).
  // dataUpdatedAt is read so each successful fetch re-renders this hook.
  void query.dataUpdatedAt;
  const isStale = data ? statusAgeSeconds(data.timestamp) > STALE_THRESHOLD_SECONDS : true;

  const error = query.error
    ? query.error instanceof Error
      ? query.error.message
      : "Failed to fetch modem status"
    : null;

  return {
    data,
    isLoading: query.isLoading || query.isPending,
    isStale,
    error,
    refresh: () => {
      void query.refetch();
    },
  };
}
