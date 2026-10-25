// =============================================================================
// What works on the VOS 5G stick (Compal RXM-G1 / SG500M2-X behind qcmd)
// -----------------------------------------------------------------------------
// Upstream QManager targets Quectel modems on OpenWRT. Some of its pages and
// controls do nothing on this stick: the backend is a stub, the AT command has
// no Compal equivalent, the daemon behind it doesn't exist here, or the
// hardware isn't there. Those are hidden rather than left looking usable.
//
// To bring one back once it is implemented, delete it from the lists below.
// Navigation (sidebar, section tabs, search) and the route guard read
// HIDDEN_ROUTES; individual cards read VOS_FEATURES.
// =============================================================================

/** Pages that don't work on this stick, and why. */
export const HIDDEN_ROUTES: Record<string, string> = {
  // Local network
  "/local-network": "No Ethernet port; the LAN address editor only pretends to save",
  "/local-network/traffic-engine": "User traffic takes the IPA hardware fast path and never reaches Linux",
  "/local-network/dpi-masking": "Same as the Traffic Engine: the IPA fast path bypasses nfqws",
  "/local-network/ip-passthrough": "Backend is a stub (no QMAP equivalent on this modem)",
  "/local-network/ttl-settings": "TTL rules miss offloaded traffic; MTU is saved but never applied",
  // Cellular
  "/cellular/custom-profiles": "Applying a SIM profile only records it; nothing is sent to the modem",
  "/cellular/settings": "SIM slot, radio power, 5G architecture and roaming are not wired to the modem",
  "/cellular/settings/network-priority": "qcmd has no translation for rat_acq_order",
  "/cellular/settings/imei-settings": "IMEI writes are deliberately blocked",
  "/cellular/settings/fplmn-settings": "Needs AT+CRSM, which qcmd blocks; the status is a stub",
  // Monitoring
  // System settings
  "/system-settings": "Units, timezone and the other General settings aren't saved; scheduled reboot needs a writable /etc",
  "/system-settings/languages": "Language-pack installs are a stub (the bundled languages still work)",
  "/system-settings/config-backup": "Restore only rewrites QManager's files and never applies modem settings",
  "/system-settings/connection-quality": "Nothing reads these settings yet",
  "/system-settings/adaptive-polling": "The poller ignores these settings",
};

/**
 * Where a hidden page sends you instead (route guard), and what a menu entry
 * for it becomes. An entry whose target is already in the same menu is dropped.
 */
export const ROUTE_REDIRECTS: Record<string, { to: string; t_key?: string; title?: string }> = {
  "/local-network": { to: "/local-network/custom-dns", t_key: "custom_dns", title: "DNS & Ad Blocking" },
  "/cellular/custom-profiles": {
    to: "/cellular/custom-profiles/connection-scenarios",
    t_key: "connection_scenarios",
    title: "Scenarios",
  },
  "/cellular/settings": { to: "/cellular/settings/apn-management", title: "APN" },
  "/system-settings": { to: "/system-settings/bandwidth-monitor" },
};

/** Cards and controls inside pages that otherwise work. */
export const VOS_FEATURES = {
  /** Band Locking: automatic band failover (no watcher daemon). */
  bandFailover: false,
  /** Tower Locking: failover, persistence and schedule (stubs). */
  towerLockExtras: false,
  /** Frequency Locking: LTE EARFCN-only lock (the modem only locks LTE to a cell). */
  lteFrequencyLock: false,
  /** APN page: MBN profile selection (AT+QMBNCFG returns ERROR here). */
  mbn: false,
  /** Custom SIM profiles: applying one changes nothing on the modem. While
   *  off, no page treats a profile as owning its settings (the backend reports
   *  the built-in "default" profile as active, which used to lock the APN page). */
  simProfiles: false,
  /** Watchdog: switching to a backup SIM (the board wires one SIM slot). */
  simFailover: false,
  /** Watchdog: recovery on poor quality (latency/loss); only lost connections trigger it here. */
  watchdogQuality: false,
  /** Watchdog: modem-crash (SSR) hold settings; vosclean restarts a crashed modem itself. */
  watchdogSsr: false,
} as const;

function normalise(path: string): string {
  const p = path.replace(/\/+$/, "");
  return p === "" ? "/" : p;
}

/** True when the page at this path is hidden on this stick. */
export function isRouteHidden(path: string): boolean {
  return normalise(path) in HIDDEN_ROUTES;
}

/** The path a link should use: itself, its redirect, or null if hidden. */
export function resolveRoute(path: string): string | null {
  const p = normalise(path);
  if (!(p in HIDDEN_ROUTES)) return path;
  return ROUTE_REDIRECTS[p]?.to ?? null;
}

interface NavLeaf {
  t_key: string;
  url: string;
}

/**
 * Sidebar groups without hidden pages. A hidden parent with a redirect points
 * at the redirect; one without points at its first visible child. Entries
 * that would duplicate a real one are dropped.
 */
export function visibleNav<T extends NavLeaf & { items?: NavLeaf[] }>(items: T[], taken = new Set<string>()): T[] {
  const real = new Set<string>(taken);
  for (const it of items) {
    if (!isRouteHidden(it.url)) real.add(normalise(it.url));
    for (const s of it.items ?? []) if (!isRouteHidden(s.url)) real.add(normalise(s.url));
  }
  const out: T[] = [];
  const used = new Set<string>(taken);
  for (const it of items) {
    const subs = (it.items ?? []).filter((s) => !isRouteHidden(s.url));
    let next: T | null;
    if (!isRouteHidden(it.url)) {
      next = { ...it, items: it.items ? subs : undefined };
    } else {
      const red = ROUTE_REDIRECTS[normalise(it.url)];
      const target = red?.to ?? subs[0]?.url;
      const rest = subs.filter((s) => normalise(s.url) !== normalise(target ?? ""));
      if (!target || (rest.length === 0 && real.has(normalise(target)) && !subs.some((s) => s.url === target))) {
        next = null; // nothing left, or its target already has its own entry
      } else if (rest.length === 0) {
        const leaf = subs.find((s) => s.url === target);
        next = { ...it, t_key: leaf?.t_key ?? red?.t_key ?? it.t_key, url: target, items: undefined };
      } else {
        next = { ...it, url: target, items: rest };
      }
    }
    if (next && !used.has(normalise(next.url))) {
      used.add(normalise(next.url));
      out.push(next);
    }
  }
  return out;
}

/** Section tabs without hidden pages (same rules as visibleNav). */
export function visibleTabs<T extends { title: string; href: string }>(items: T[]): T[] {
  const real = new Set(items.filter((i) => !isRouteHidden(i.href)).map((i) => normalise(i.href)));
  const out: T[] = [];
  const used = new Set<string>();
  for (const it of items) {
    let next: T | null = it;
    if (isRouteHidden(it.href)) {
      const red = ROUTE_REDIRECTS[normalise(it.href)];
      next = red && !real.has(normalise(red.to)) ? { ...it, href: red.to, title: red.title ?? it.title } : null;
    }
    if (next && !used.has(normalise(next.href))) {
      used.add(normalise(next.href));
      out.push(next);
    }
  }
  return out;
}
