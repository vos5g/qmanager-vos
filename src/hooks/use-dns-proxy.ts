"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { authFetch } from "@/lib/auth-fetch";

// vosclean: Ad Blocking + Encrypted DNS, both served by QManager's DNS proxy
// (backend: pkg/api/zz_vosclean_dnsproxy.go, pkg/dnsproxy).

const ENDPOINT = "/cgi-bin/quecmanager/network/dns_proxy.sh";
const KEY = ["dns-proxy"];

export interface EncryptedSettings {
  enabled: boolean;
  /** Provider id from `providers`, or "custom" */
  provider: string;
  protocol: "doh" | "dot";
  /** https://… (DoH) or tls://host[:port] (DoT) when provider is "custom" */
  custom_url: string;
  /** Use plain DNS when the encrypted resolver can't be reached */
  fallback: boolean;
}

export interface BlockingSettings {
  enabled: boolean;
  lists: string[];
  custom_urls: string[];
  allowlist: string[];
  denylist: string[];
}

export interface BlockingState extends BlockingSettings {
  domain_count: number;
  updated_at: number;
  update_error: string;
  updating: boolean;
  /** The chosen lists changed and haven't been downloaded yet */
  stale: boolean;
}

export interface DnsProxyStats {
  queries: number;
  blocked: number;
  cache_hits: number;
  encrypted: number;
  fallback: number;
  failures: number;
  latency_ms: number;
  last_error: string;
  last_error_at: number;
  top_blocked: { name: string; count: number }[] | null;
  since: number;
  upstream: string;
}

export type ResolverProtocol = "plain" | "doh" | "dot";

/** The DNS Servers card's choice (save with `saveResolver`). */
export interface ResolverSettings {
  mode: "carrier" | "custom";
  /** Provider id from `providers`, or "custom" */
  provider: string;
  protocol: ResolverProtocol;
  /** Plain servers for provider "custom" + protocol "plain" */
  servers: string[];
  /** https://… or tls://… for provider "custom" + encryption */
  custom_url: string;
  fallback: boolean;
}

export interface DnsProvider {
  id: string;
  label: string;
  ipv4: string[];
  /** Also answers unencrypted DNS */
  plain: boolean;
  /** DNS over HTTPS URL */
  doh: string;
  /** DNS over TLS address, tls://host */
  dot: string;
}

export interface DnsProxyStatus {
  success: boolean;
  encrypted: EncryptedSettings;
  blocking: BlockingState;
  providers: DnsProvider[];
  resolver: ResolverSettings;
  blocklists: { id: string; label: string; count: number }[];
  /** Proxy listening */
  running: boolean;
  /** vosclean is sending DNS to the proxy right now */
  redirect_active: boolean;
  /** "Custom DNS" or "Carrier DNS": the plain resolvers behind the proxy */
  plain_upstream: string;
  apply_error: string;
  clock_ok: boolean;
  stats?: DnsProxyStats;
}

async function post<T>(body: object): Promise<T> {
  const res = await authFetch(ENDPOINT, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const data = await res.json();
  if (!data.success) throw new Error(data.detail || data.error || "Request failed");
  return data;
}

export function useDnsProxy() {
  const qc = useQueryClient();
  const query = useQuery<DnsProxyStatus>({
    queryKey: KEY,
    queryFn: async () => {
      const res = await authFetch(ENDPOINT);
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      return res.json();
    },
    // Poll fast while a blocklist download runs, otherwise just keep stats fresh.
    refetchInterval: (q) => (q.state.data?.blocking.updating ? 3_000 : 15_000),
  });
  const onSuccess = (d: DnsProxyStatus) => qc.setQueryData(KEY, d);
  const save = useMutation({
    mutationFn: (s: { encrypted?: EncryptedSettings; blocking?: BlockingSettings }) =>
      post<DnsProxyStatus>({ action: "save", ...s }),
    onSuccess,
  });
  const saveResolver = useMutation({
    mutationFn: (r: ResolverSettings) => post<DnsProxyStatus>({ action: "save_resolver", ...r }),
    onSuccess,
  });
  const updateLists = useMutation({
    mutationFn: () => post<DnsProxyStatus>({ action: "update_lists" }),
    onSuccess,
  });
  const check = useMutation({
    mutationFn: (domain: string) =>
      post<{ domain: string; blocked: boolean; allowlisted: boolean; blocking_enabled: boolean }>({
        action: "check",
        domain,
      }),
  });
  return {
    data: query.data ?? null,
    isLoading: query.isLoading,
    error: query.error,
    save,
    saveResolver,
    updateLists,
    check,
  };
}

/** One domain per line (or comma/space separated) → trimmed list. */
export function parseDomainList(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim().toLowerCase())
    .filter(Boolean);
}
