"use client";

import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Field,
  FieldContent,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { SaveButton, useSaveFlash } from "@/components/ui/save-button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import {
  useDnsProxy,
  type DnsProxyStatus,
  type ResolverProtocol,
  type ResolverSettings,
} from "@/hooks/use-dns-proxy";
import { DnsProxyBadge, StatRow, type DnsProxyBadgeState } from "./dns-proxy-badge";

// =============================================================================
// DNS Servers — who answers lookups for every device on the stick
// =============================================================================
// One choice: the carrier's DNS, a provider, or custom servers, optionally
// encrypted (DoH/DoT). Plain servers are applied by vosclean's redirect;
// encryption runs through QManager's DNS proxy, with the provider's plain
// servers as the fallback. Backend: network/dns_proxy.sh `save_resolver`.
// =============================================================================

const CARRIER = "carrier";
const IPV4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;

interface FormState {
  /** "carrier", a provider id, or "custom" */
  choice: string;
  protocol: ResolverProtocol;
  dns1: string;
  dns2: string;
  url: string;
  fallback: boolean;
}

function toForm(r: ResolverSettings): FormState {
  return {
    choice: r.mode === "carrier" ? CARRIER : r.provider,
    protocol: r.protocol,
    dns1: r.servers[0] ?? "",
    dns2: r.servers[1] ?? "",
    url: r.custom_url.replace(/^(https|tls):\/\//, ""),
    fallback: r.fallback,
  };
}

function badge(d: DnsProxyStatus, t: (k: string) => string): { state: DnsProxyBadgeState; label?: string } {
  const r = d.resolver;
  if (r.mode === "carrier") return { state: "off", label: t("dns_servers.badge_carrier") };
  if (r.protocol === "plain") return { state: "off", label: t("dns_servers.badge_plain") };
  if (!d.running || !d.redirect_active) return { state: "warning", label: t("dns_proxy.status_not_running") };
  if (!d.clock_ok) return { state: "warning", label: t("dns_proxy.status_waiting_clock") };
  const s = d.stats;
  if (s && s.last_error_at && Date.now() / 1000 - s.last_error_at < 120 && s.fallback > 0) {
    return { state: "warning", label: t("dns_proxy.status_fallback") };
  }
  return { state: "active", label: t("dns_proxy.status_encrypted") };
}

function ServersForm({ data }: { data: DnsProxyStatus }) {
  const { t } = useTranslation("local-network");
  const { saveResolver } = useDnsProxy();
  const { saved, markSaved } = useSaveFlash();
  const [form, setForm] = useState<FormState>(() => toForm(data.resolver));
  const set = (p: Partial<FormState>) => setForm((f) => ({ ...f, ...p }));

  const isCarrier = form.choice === CARRIER;
  const isCustom = form.choice === "custom";
  const provider = data.providers.find((p) => p.id === form.choice);
  const plainAllowed = isCustom || (provider?.plain ?? true);
  const protocol: ResolverProtocol = !plainAllowed && form.protocol === "plain" ? "doh" : form.protocol;
  const encrypted = !isCarrier && protocol !== "plain";

  const dns1Err = isCustom && protocol === "plain" && !IPV4.test(form.dns1.trim());
  const dns2Err = isCustom && protocol === "plain" && form.dns2.trim() !== "" && !IPV4.test(form.dns2.trim());
  const url = form.url.trim();
  const urlErr = isCustom && encrypted && !/^[a-z0-9.-]+\.[a-z]{2,}(:\d{1,5})?(\/\S*)?$/i.test(url);
  const invalid = dns1Err || dns2Err || urlErr;

  const request = (): ResolverSettings => ({
    mode: isCarrier ? "carrier" : "custom",
    provider: isCarrier ? "cloudflare" : form.choice,
    protocol,
    servers: isCustom && protocol === "plain" ? [form.dns1.trim(), form.dns2.trim()].filter(Boolean) : [],
    custom_url: isCustom && encrypted ? `${protocol === "dot" ? "tls" : "https"}://${url}` : "",
    fallback: form.fallback,
  });
  const initial = toForm(data.resolver);
  const dirty = isCarrier
    ? initial.choice !== CARRIER
    : JSON.stringify({ ...form, protocol }) !== JSON.stringify(initial);

  const fallbackTarget = isCustom || !provider?.plain ? t("dns_servers.fallback_carrier") : provider.label;
  const s = data.stats;
  const showStats = data.resolver.mode === "custom" && data.resolver.protocol !== "plain" && data.running && s;

  const onSave = () =>
    saveResolver.mutate(request(), {
      onSuccess: () => {
        markSaved();
        toast.success(t("dns_servers.toast_saved"));
      },
      onError: (e) => toast.error(e instanceof Error ? e.message : t("dns_proxy.toast_error")),
    });

  return (
    <form
      className="grid gap-5"
      onSubmit={(e) => {
        e.preventDefault();
        onSave();
      }}
    >
      <FieldGroup>
        <div className="grid gap-4 @md/card:grid-cols-2">
          <Field>
            <FieldLabel htmlFor="dns-choice">{t("dns_servers.provider")}</FieldLabel>
            <Select value={form.choice} onValueChange={(v) => set({ choice: v })}>
              <SelectTrigger id="dns-choice">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={CARRIER}>{t("dns_servers.carrier")}</SelectItem>
                {data.providers.map((p) => (
                  <SelectItem key={p.id} value={p.id}>
                    {p.label}
                  </SelectItem>
                ))}
                <SelectItem value="custom">{t("dns.provider_custom")}</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          {!isCarrier && (
            <Field>
              <FieldLabel>{t("dns_servers.encryption")}</FieldLabel>
              <ToggleGroup
                type="single"
                variant="outline"
                value={protocol}
                onValueChange={(v) => v && set({ protocol: v as ResolverProtocol })}
                aria-label={t("dns_servers.encryption")}
              >
                <ToggleGroupItem value="plain" className="px-3" disabled={!plainAllowed}>
                  {t("dns_servers.enc_off")}
                </ToggleGroupItem>
                <ToggleGroupItem value="doh" className="px-3">
                  HTTPS
                </ToggleGroupItem>
                <ToggleGroupItem value="dot" className="px-3">
                  TLS
                </ToggleGroupItem>
              </ToggleGroup>
            </Field>
          )}
        </div>

        <FieldDescription>
          {isCarrier
            ? t("dns_servers.hint_carrier")
            : encrypted
              ? t("dns_servers.hint_encrypted")
              : !plainAllowed
                ? t("dns_servers.hint_encrypted_only")
                : t("dns_servers.hint_plain")}
        </FieldDescription>

        {provider && !isCarrier && protocol === "plain" && (
          <p className="text-sm tabular-nums text-muted-foreground">{provider.ipv4.join(", ")}</p>
        )}

        {isCustom && protocol === "plain" && (
          <div className="grid gap-4 @md/card:grid-cols-2">
            <Field>
              <FieldLabel htmlFor="dns-1">{t("dns.label_primary_dns")}</FieldLabel>
              <Input
                id="dns-1"
                value={form.dns1}
                onChange={(e) => set({ dns1: e.target.value })}
                placeholder="9.9.9.9"
                inputMode="decimal"
                className="tabular-nums"
                aria-invalid={(dns1Err && form.dns1 !== "") || undefined}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="dns-2">
                {t("dns.label_secondary_dns")}
                <span className="text-muted-foreground">{t("dns.label_tertiary_dns_optional_suffix")}</span>
              </FieldLabel>
              <Input
                id="dns-2"
                value={form.dns2}
                onChange={(e) => set({ dns2: e.target.value })}
                placeholder="149.112.112.112"
                inputMode="decimal"
                className="tabular-nums"
                aria-invalid={dns2Err || undefined}
              />
            </Field>
            {((dns1Err && form.dns1 !== "") || dns2Err) && (
              <FieldError className="@md/card:col-span-2">{t("dns_servers.bad_ip")}</FieldError>
            )}
          </div>
        )}

        {isCustom && encrypted && (
          <Field>
            <FieldLabel htmlFor="dns-url">{t("dns_servers.server_address")}</FieldLabel>
            <div className="flex items-center rounded-md border bg-transparent shadow-xs focus-within:ring-[3px] focus-within:ring-ring/50">
              <span className="pl-3 text-sm text-muted-foreground select-none">
                {protocol === "dot" ? "tls://" : "https://"}
              </span>
              <Input
                id="dns-url"
                value={form.url}
                onChange={(e) => set({ url: e.target.value.replace(/^(https|tls):\/\//i, "") })}
                placeholder={protocol === "dot" ? "dns.example.com" : "dns.example.com/dns-query"}
                spellCheck={false}
                autoCapitalize="none"
                className="border-0 pl-0.5 shadow-none focus-visible:ring-0"
                aria-invalid={(urlErr && url !== "") || undefined}
              />
            </div>
            {urlErr && url !== "" && <FieldError>{t("dns_servers.bad_url")}</FieldError>}
          </Field>
        )}

        {encrypted && (
          <Field orientation="horizontal">
            <Switch id="dns-fallback" checked={form.fallback} onCheckedChange={(v) => set({ fallback: v })} />
            <FieldContent>
              <FieldLabel htmlFor="dns-fallback">{t("dns_proxy.enc_fallback")}</FieldLabel>
              <FieldDescription>{t("dns_servers.fallback_hint", { plain: fallbackTarget })}</FieldDescription>
            </FieldContent>
          </Field>
        )}
      </FieldGroup>

      {showStats && s && (
        <div className="grid gap-2 rounded-lg border bg-muted/30 p-4">
          <StatRow label={t("dns_proxy.stat_resolver")} value={s.upstream} />
          <StatRow label={t("dns_proxy.stat_latency")} value={s.latency_ms ? `${s.latency_ms} ms` : "–"} />
          <StatRow label={t("dns_proxy.stat_encrypted")} value={s.encrypted.toLocaleString()} />
          <StatRow label={t("dns_proxy.stat_fallback")} value={s.fallback.toLocaleString()} />
          {s.last_error && (
            <p className="pt-1 text-xs text-muted-foreground break-words">
              {t("dns_proxy.stat_last_error", {
                time: new Date(s.last_error_at * 1000).toLocaleTimeString(),
                error: s.last_error,
              })}
            </p>
          )}
        </div>
      )}

      <SaveButton
        type="submit"
        isSaving={saveResolver.isPending}
        saved={saved}
        label={t("actions.apply", { ns: "common" })}
        className="w-fit"
        disabled={!dirty || invalid}
      />
    </form>
  );
}

export default function DNSServersCard() {
  const { t } = useTranslation("local-network");
  const { data, isLoading } = useDnsProxy();
  const b = data ? badge(data, t) : null;
  return (
    <Card className="@container/card">
      <CardHeader>
        <CardTitle>{t("dns_servers.title")}</CardTitle>
        <CardDescription>{t("dns_servers.description")}</CardDescription>
        {b && (
          <CardAction>
            <DnsProxyBadge state={b.state} label={b.label} />
          </CardAction>
        )}
      </CardHeader>
      <CardContent>
        {isLoading || !data ? (
          <div className="grid gap-4">
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-4 w-3/4" />
            <Skeleton className="h-9 w-24" />
          </div>
        ) : (
          // Re-seed the form when the saved choice changes on the stick.
          <ServersForm key={JSON.stringify(data.resolver)} data={data} />
        )}
      </CardContent>
    </Card>
  );
}
