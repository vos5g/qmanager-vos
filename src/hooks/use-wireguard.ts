"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { authFetch } from "@/lib/auth-fetch";

const ENDPOINT = "/cgi-bin/quecmanager/vpn/wireguard.sh";
const POLL_MS = 4000;

export type WgEngine = "userspace" | "kernel";
export type WgRoute = "lan" | "all";
export type WgPhase = "disconnected" | "starting" | "connecting" | "up" | "error";

export interface WgConfigSummary {
  name: string;
  error?: string;
  addresses?: string[];
  dns?: string[];
  endpoint?: string;
  peer_count?: number;
  allowed_ips?: string[];
  full_tunnel?: boolean;
  engine?: WgEngine;
  route?: WgRoute;
  kill_switch?: boolean;
  mtu?: number;
  public_key?: string;
}

export interface WgLive {
  rx_bytes: number;
  tx_bytes: number;
  endpoint: string;
  listen_port: number;
  last_handshake?: number;
  handshake_age_s?: number;
}

export interface WgStatus {
  active: string | null;
  phase: WgPhase;
  service_running: boolean;
  error?: string;
  since?: number;
  engine?: WgEngine;
  route?: WgRoute;
  kill_switch?: boolean;
  blocking?: boolean;
  live?: WgLive;
}

export interface WgState {
  success: boolean;
  configs: WgConfigSummary[];
  status: WgStatus;
  kernel_loaded: boolean;
  kernel_present: boolean;
  netbird_running: boolean;
}

/** The stick's options for a tunnel (stored in the config file). */
export interface WgOptions {
  engine: WgEngine;
  route: WgRoute;
  kill_switch: boolean;
}

export interface WgPeerForm {
  public_key: string;
  preshared_key: string;
  clear_preshared_key: boolean;
  has_preshared_key: boolean; // from the server, for the edit form
  endpoint: string;
  allowed_ips: string; // comma-separated in the form
  keepalive: string;
}

export interface WgConfigForm extends WgOptions {
  name: string;
  private_key: string;
  generate_key: boolean;
  has_private_key: boolean;
  public_key: string;
  addresses: string;
  dns: string;
  mtu: string;
  listen_port: string;
  peers: WgPeerForm[];
}

/** Server reply for one config (GET ?name=). Secrets are never included. */
interface WgEditView extends WgOptions {
  name: string;
  addresses: string[] | null;
  dns: string[] | null;
  mtu: number;
  listen_port: number;
  has_private_key: boolean;
  public_key: string;
  peers: {
    public_key: string;
    endpoint: string;
    allowed_ips: string[] | null;
    keepalive: number;
    has_preshared_key: boolean;
  }[];
}

export interface WgActionResult {
  success: boolean;
  error?: string;
  detail?: string;
  warning?: string;
  saved?: string;
  public_key?: string;
  ignored?: string[] | null;
}

export const DEFAULT_OPTIONS: WgOptions = {
  engine: "userspace",
  route: "lan",
  kill_switch: true,
};

export function emptyPeer(): WgPeerForm {
  return {
    public_key: "",
    preshared_key: "",
    clear_preshared_key: false,
    has_preshared_key: false,
    endpoint: "",
    allowed_ips: "0.0.0.0/0",
    keepalive: "25",
  };
}

export function emptyForm(): WgConfigForm {
  return {
    ...DEFAULT_OPTIONS,
    name: "",
    private_key: "",
    generate_key: false,
    has_private_key: false,
    public_key: "",
    addresses: "",
    dns: "",
    mtu: "",
    listen_port: "",
    peers: [emptyPeer()],
  };
}

const list = (s: string) =>
  s
    .split(/[,\s]+/)
    .map((x) => x.trim())
    .filter(Boolean);

const int = (s: string) => {
  const n = parseInt(s, 10);
  return Number.isFinite(n) ? n : 0;
};

export function formatBytes(n: number): string {
  if (!n) return "0 B";
  const u = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(u.length - 1, Math.floor(Math.log(n) / Math.log(1024)));
  return `${(n / 1024 ** i).toFixed(i === 0 ? 0 : 1)} ${u[i]}`;
}

/** Saved WireGuard configs, the tunnel's status, and the actions on them. */
export function useWireGuard() {
  const [state, setState] = useState<WgState | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null); // "connect:Name", "save", ...
  const busyRef = useRef(false);

  const load = useCallback(async () => {
    try {
      const res = await authFetch(ENDPOINT);
      const data = (await res.json()) as WgState & { detail?: string; error?: string };
      if (!data.success) throw new Error(data.detail || data.error || "failed");
      setState(data);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    void load();
    const id = setInterval(() => {
      if (!busyRef.current && document.visibilityState === "visible") void load();
    }, POLL_MS);
    return () => clearInterval(id);
  }, [load]);

  const post = useCallback(
    async (key: string, body: Record<string, unknown>): Promise<WgActionResult> => {
      setBusy(key);
      busyRef.current = true;
      try {
        const res = await authFetch(ENDPOINT, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        });
        const data = (await res.json()) as WgState & WgActionResult;
        if (data.configs) setState(data);
        return data;
      } catch (e) {
        return { success: false, error: "network", detail: e instanceof Error ? e.message : String(e) };
      } finally {
        busyRef.current = false;
        setBusy(null);
      }
    },
    [],
  );

  const getConfig = useCallback(async (name: string): Promise<WgConfigForm | null> => {
    try {
      const res = await authFetch(`${ENDPOINT}?name=${encodeURIComponent(name)}`);
      const data = (await res.json()) as { success: boolean; config?: WgEditView };
      if (!data.success || !data.config) return null;
      const c = data.config;
      return {
        name: c.name,
        engine: c.engine,
        route: c.route,
        kill_switch: c.kill_switch,
        private_key: "",
        generate_key: false,
        has_private_key: c.has_private_key,
        public_key: c.public_key,
        addresses: (c.addresses ?? []).join(", "),
        dns: (c.dns ?? []).join(", "),
        mtu: c.mtu ? String(c.mtu) : "",
        listen_port: c.listen_port ? String(c.listen_port) : "",
        peers: c.peers.map((p) => ({
          public_key: p.public_key,
          preshared_key: "",
          clear_preshared_key: false,
          has_preshared_key: p.has_preshared_key,
          endpoint: p.endpoint,
          allowed_ips: (p.allowed_ips ?? []).join(", "),
          keepalive: p.keepalive ? String(p.keepalive) : "",
        })),
      };
    } catch {
      return null;
    }
  }, []);

  const saveConfig = useCallback(
    (form: WgConfigForm, originalName?: string) =>
      post("save", {
        action: "save",
        original_name: originalName ?? "",
        name: form.name.trim(),
        private_key: form.generate_key ? "" : form.private_key.trim(),
        generate_key: form.generate_key,
        addresses: list(form.addresses),
        dns: list(form.dns),
        mtu: int(form.mtu),
        listen_port: int(form.listen_port),
        engine: form.engine,
        route: form.route,
        kill_switch: form.kill_switch,
        peers: form.peers.map((p) => ({
          public_key: p.public_key.trim(),
          preshared_key: p.clear_preshared_key ? "" : p.preshared_key.trim(),
          clear_preshared_key: p.clear_preshared_key,
          endpoint: p.endpoint.trim(),
          allowed_ips: list(p.allowed_ips),
          keepalive: int(p.keepalive),
        })),
      }),
    [post],
  );

  const importConfig = useCallback(
    (name: string, text: string, opts: WgOptions) =>
      post("import", { action: "import", name: name.trim(), text, ...opts }),
    [post],
  );

  const deleteConfig = useCallback(
    (name: string) => post(`delete:${name}`, { action: "delete", name }),
    [post],
  );

  const connect = useCallback(
    (name: string) => post(`connect:${name}`, { action: "connect", name }),
    [post],
  );

  const disconnect = useCallback(() => post("disconnect", { action: "disconnect" }), [post]);

  return {
    state,
    error,
    busy,
    reload: load,
    getConfig,
    saveConfig,
    importConfig,
    deleteConfig,
    connect,
    disconnect,
  };
}
