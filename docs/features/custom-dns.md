# Custom DNS

Custom DNS lets users override the carrier-assigned resolver addresses on both IPv4 and IPv6 simultaneously. IPv4 addresses are delivered to LAN clients via dnsmasq `dhcp_option 6`; IPv6 addresses are announced to LAN clients via odhcpd's RA RDNSS (RFC 6106) and DHCPv6 option 23. A provider-preset dropdown (Cloudflare, Google, Quad9, AdGuard, ControlD) fills all four address fields at once; "Custom" exposes them for direct editing.

## Quick Reference

| Surface | Path |
|---|---|
| CGI endpoint | `network/dns.sh` |
| Hook | `hooks/use-dns-settings.ts` |
| Provider presets | `components/local-network/custom-dns/dns-providers.ts` |
| Component | `components/local-network/custom-dns/custom-dns-card.tsx` |
| Mode state file | `/etc/qmanager/dns_mode` |
| IPv6 UCI path | `dhcp.lan.dns` (list) |
| IPv4 UCI path | `dhcp.lan.dhcp_option` (value `"6,addr1,addr2"`) |
| Reboot required | No |

## Dual Delivery Paths

Two completely separate delivery mechanisms handle the two address families. They share a single CGI endpoint and a single enable/disable toggle, but the kernel paths are different.

### IPv4 — dnsmasq `dhcp_option`

IPv4 DNS is delivered as DHCP option 6 via dnsmasq.

On enable, the script writes:

```sh
# DHCP option 6 — tells clients to use these DNS servers directly
uci set dhcp.lan.dhcp_option="6,$dns1,$dns2,$dns3"

# Upstream servers — dnsmasq itself resolves through these (UCI list 'server'
# takes precedence over resolv-file).  Ensures clients that use the router as
# their DNS resolver also get custom DNS instead of carrier upstream.
uci -q delete dhcp.lan_dns.server
for ip in $dns1 $dns2 $dns3; do uci add_list dhcp.lan_dns.server="$ip"; done
for ip6 in $dns1v6 $dns2v6; do uci add_list dhcp.lan_dns.server="$ip6"; done

uci commit dhcp
/etc/init.d/dnsmasq restart
```

On disable, it restores the carrier addresses read from `/tmp/resolv.conf.d/resolv.conf.auto` (IPv4-only file), removes the UCI `server` list (so dnsmasq falls back to its `resolv-file`), and restarts dnsmasq.

The `$nic` value is either `lan` or `lan_bind4`, determined at each request by querying `AT+QMAP="MPDN_RULE"`. If any rule has `enabled=1`, the NIC is `lan_bind4`; otherwise it is `lan`. This information is returned to the frontend for display purposes, but the IPv4 DNS configuration always targets `dhcp.lan` — dnsmasq serves DHCP option 6 from the `dhcp.lan` section regardless of whether IP Passthrough has created a `dhcp.lan_bind4` section.

### IPv6 — odhcpd RA RDNSS + DHCPv6

IPv6 DNS is delivered via Router Advertisements (RFC 6106 RDNSS option) and DHCPv6 option 23, both served by odhcpd.

On enable, the script rebuilds the `dhcp.lan.dns` UCI list:
```sh
uci -q delete dhcp.lan.dns
uci add_list dhcp.lan.dns="$dns1v6"
uci add_list dhcp.lan.dns="$dns2v6"
uci commit dhcp
/etc/init.d/odhcpd reload
```

On disable, `uci -q delete dhcp.lan.dns` clears the list and `odhcpd reload` picks it up.

> ℹ️ NOTE: `odhcpd reload` sends SIGHUP. Using `restart` instead would briefly drop RA announcements, causing a gap in router advertisements to IPv6 clients. `reload` avoids this.

## Why IPv6 Always Targets `dhcp.lan`

The IPv6 list is hardcoded to `dhcp.lan`, never `dhcp.$nic`. odhcpd serves RA and DHCPv6 only from the `lan` section — it does not serve from `dhcp.lan_bind4` even when MPDN is active. Writing to `dhcp.lan_bind4.dns` would have no effect on RA/DHCPv6 delivery.

## Why Disable Clears IPv6 But Does Not Restore Carrier IPv6

`/tmp/resolv.conf.d/resolv.conf.auto` is IPv4-only. The modem's firmware does not write carrier IPv6 nameservers there. Capturing carrier IPv6 would require parsing NDP router advertisements, which is not feasible in BusyBox shell at CGI runtime.

When the IPv6 list is cleared, clients fall back to using the router itself as the RDNSS (the default behavior before any DNS was configured), which typically resolves to the carrier-assigned IPv6 resolver via the modem's own upstream path. This is the same state as a factory-fresh device.

## IPv6 Address Validator (BusyBox Two-Stage Gate)

The frontend validates IPv6 structurally using a regex. The backend adds a BusyBox-compatible sanitization fence using two sequential `case` checks — BusyBox `sh` does not support regex in `[[ ]]`:

```sh
# Stage 1: must contain at least one colon (IPv6 mandatory)
case "$_dns6" in
    *:*) ;;
    *) cgi_error "invalid_dns" ...; exit 0 ;;
esac

# Stage 2: only hex digits and colons allowed
case "$_dns6" in
    *[!0-9a-fA-F:]*) cgi_error "invalid_dns" ...; exit 0 ;;
esac
```

Both IPv4 and IPv6 validation failures reuse the same `invalid_dns` error code. The `detail` field on the error response includes the offending address.

## The "At Least One Address" Guard

When enabling, the backend requires that at least one address across all five fields (`dns1`, `dns2`, `dns3`, `dns1v6`, `dns2v6`) is non-empty. You can configure IPv4-only (no IPv6 fields), IPv6-only (no IPv4 fields), or dual-stack. Submitting a mode-enabled request with all five fields blank returns `missing_field`.

## Provider Preset Model

`components/local-network/custom-dns/dns-providers.ts` defines the `DNS_PROVIDERS` array, the `DnsProvider` interface, and the `matchProvider` function.

Each `DnsProvider` carries exactly two IPv4 and two IPv6 addresses. The providers are:

| ID | Name | IPv4 Primary | IPv4 Secondary |
|---|---|---|---|
| `cloudflare` | Cloudflare | `1.1.1.1` | `1.0.0.1` |
| `google` | Google | `8.8.8.8` | `8.8.4.4` |
| `quad9` | Quad9 | `9.9.9.9` | `149.112.112.112` |
| `adguard` | AdGuard | `94.140.14.14` | `94.140.15.15` |
| `controld` | ControlD | `76.76.2.0` | `76.76.10.0` |

The sentinel `CUSTOM_PROVIDER_ID = "custom"` is used when no preset matches.

### `matchProvider` Round-Trip Detection

When the card loads, it calls `matchProvider(dns1, dns2, dns3, dns1v6, dns2v6)` to detect whether the currently-saved addresses correspond to a known preset. Detection rules:

- If `dns3` is non-empty, returns `"custom"` immediately — presets only define two IPv4 addresses.
- Iterates `DNS_PROVIDERS`, comparing `dns1`/`dns2` exactly to the preset's IPv4 pair and `dns1v6`/`dns2v6` case-insensitively (via `normV6`) to the IPv6 pair.
- Returns the matching provider's `id`, or `"custom"` if none match.

This is order-sensitive: `dns1` maps to the primary, `dns2` to the secondary. If the user has swapped them, `matchProvider` returns `"custom"`.

Selecting a provider in the card fills all four fields and shows a read-only address summary. Switching to "Custom" reveals editable inputs.

## Mode State File

`/etc/qmanager/dns_mode` contains the literal string `enabled` or `disabled`. It is read by the GET handler to populate the `mode` field in the response. It is written atomically (`echo "enabled" > /etc/qmanager/dns_mode`) after UCI commit succeeds.

The file is created by the first POST; a fresh install with no prior POST will have no file. The GET handler defaults to `"disabled"` if the file is absent (`cat ... || echo "disabled"`).

## IPv4 NIC Targeting (Resolved 2026-06-26)

**Prior behavior (bug):** When an MPDN rule was active, `get_nic()` returned `lan_bind4` and the enable path wrote IPv4 DNS to `dhcp.lan_bind4.dhcp_option`. However, dnsmasq only serves `dhcp.lan` — no dnsmasq instance exists for `lan_bind4`. Clients never received the custom IPv4 DNS via DHCP option 6, falling back to carrier DNS.

**Fix:** All IPv4 DNS paths now hardcode `dhcp.lan`, matching the disable path and IPv6 path which already used `dhcp.lan` exclusively. The `get_nic()` function still runs and `$nic` is still returned to the frontend for display, but it no longer controls the UCI target for IPv4 DNS writes.

**Verification:** `uci set dhcp.lan.dhcp_option` → dnsmasq serves `dhcp-option=lan,6,...` → clients receive custom DNS via DHCP. Confirmed on-device 2026-06-26 with Quad9 (9.9.9.9 / 149.112.112.112).

## Hook Contract

`hooks/use-dns-settings.ts` exports `useDnsSettings()` returning:

| Field | Type | Description |
|---|---|---|
| `data` | `DnsSettingsData \| null` | Fetched state; `null` before first load |
| `isLoading` | `boolean` | True during initial fetch |
| `isSaving` | `boolean` | True during save |
| `error` | `string \| null` | Resolved error message |
| `saveDns` | `(SaveDnsParams) => Promise<boolean>` | Apply new settings; silent re-fetch on success |
| `refresh` | `() => void` | Re-fetch without showing the loading state |

`DnsSettingsData` parses `currentDNS` and `currentDNS6` (both comma-separated strings from the backend) into individual `dns1`/`dns2`/`dns3` and `dns1v6`/`dns2v6` fields. `SaveDnsParams` mirrors the POST body exactly (all six address fields + `mode` + `nic`).

## VOS build: DNS Servers card, encrypted DNS and ad blocking

On the VOS stick the page is **DNS & Ad Blocking** with two cards, both served by `network/dns_proxy.sh` (`pkg/api/zz_vosclean_dnsproxy.go`, hook `use-dns-proxy.ts`). The upstream Custom DNS card, `dns-providers.ts` and `use-dns-settings.ts` are gone; `network/dns.sh` still answers for compatibility.

* **DNS Servers** — one choice: carrier, a provider from `dnsproxy.Providers`, or custom; encryption Off / HTTPS / TLS (Mullvad is encrypted-only). POST `{action:"save_resolver", mode, provider, protocol, servers, custom_url, fallback}`. Plain servers are written to `custom_dns.json` (a provider's plain IPs double as the fallback when encrypted); encryption sets the proxy's encrypted upstream. GET returns the current choice as `resolver`.
* **Ad Blocking** — POST `{action:"save", blocking:{enabled, lists, custom_urls, allowlist, denylist}}`, `{action:"update_lists"}`, `{action:"check", domain}`. Lists download in the background (`blocking.updating`), weekly refresh, compiled list in `/data/vosclean/qm/etc/blocklist.txt`.

**Data path:** `pkg/dnsproxy` listens on `127.0.0.1:5335` (UDP+TCP) while encryption or blocking is on. vosclean's `dns.sh` (`DNS_PROXY=yes` in `/data/vosclean/etc/dns.conf`) redirects dnsmasq's upstream queries there; the proxy's own sockets carry `SO_MARK 0xca6d`, which the redirect exempts.

**Invariants**

* The redirect follows a heartbeat (`/tmp/qmanager_dnsproxy.alive`, every 20 s). Stale (>90 s) or port not listening → vosclean falls back to plain servers within a minute. `StopDNSProxy` runs `vosclean dns` on shutdown.
* Before the clock is set (year < 2025) the proxy uses plain DNS: certificates can't be checked, and NTP itself needs DNS.
* `applyCustomDNS` (upstream `dns.sh` handler) keeps `DNS_PROXY` via `dnsProxyWanted()` so it never drops the redirect.
* Save failures roll back to the previous working setup.
* Clients with their own DNS (Private DNS, iCloud Private Relay, browser DoH) and a WireGuard config's own DNS bypass the proxy.

### Pause and history (v1.2)

* `pause {minutes}` (0 = until resumed) / `resume`: the proxy skips the blocklist until then. The end time is saved in `dns_proxy.json` (`paused_until`, -1 = until resumed), so a restart keeps it.
* History (`zz_vosclean_dnsstats.go`): every 30 s the proxy's counters since the last sample are added to today's bucket, and its per-name blocked counts are drained into a persistent top list (trimmed to 1000 names). This is stored in `/data/vosclean/qm/etc/dns_stats.json`, written every 10 minutes and on shutdown, and keeps 30 days; the UI shows 14. `reset_stats` clears it.
* `vosclean_ready` is false when `/data/vosclean/bin/dns.sh` predates `DNS_PROXY`; the page then asks for an `install.sh` run.
