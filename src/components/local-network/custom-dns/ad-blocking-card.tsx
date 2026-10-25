"use client";

import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChevronDownIcon, RefreshCwIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
  FieldLegend,
  FieldSet,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { SaveButton, useSaveFlash } from "@/components/ui/save-button";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";
import { parseDomainList, useDnsProxy, type DnsProxyStatus } from "@/hooks/use-dns-proxy";
import { DnsProxyBadge, StatRow, type DnsProxyBadgeState } from "./dns-proxy-badge";

const DOMAIN_RE = /^(\*\.)?([a-z0-9_-]{1,63}\.)+[a-z0-9-]*[a-z][a-z0-9-]*\.?$/i;

function blockingBadge(d: DnsProxyStatus, t: (k: string) => string): { state: DnsProxyBadgeState; label?: string } {
  const b = d.blocking;
  if (!b.enabled) return { state: "off" };
  if (!d.running || !d.redirect_active) return { state: "warning", label: t("dns_proxy.status_not_running") };
  if (b.updating && b.domain_count === 0) return { state: "warning", label: t("dns_proxy.status_downloading") };
  if (b.domain_count === 0 && b.denylist.length === 0) return { state: "warning", label: t("dns_proxy.status_no_list") };
  return { state: "active", label: t("dns_proxy.status_blocking") };
}

interface FormState {
  enabled: boolean;
  lists: string[];
  customURLs: string;
  allowlist: string;
  denylist: string;
}

function toForm(d: DnsProxyStatus): FormState {
  return {
    enabled: d.blocking.enabled,
    lists: d.blocking.lists,
    customURLs: d.blocking.custom_urls.join("\n"),
    allowlist: d.blocking.allowlist.join("\n"),
    denylist: d.blocking.denylist.join("\n"),
  };
}

function badDomains(text: string) {
  return parseDomainList(text).filter((d) => !DOMAIN_RE.test(d));
}

function badURLs(text: string) {
  return text
    .split(/\s+/)
    .filter(Boolean)
    .filter((u) => !/^https?:\/\/[^\s/]+/i.test(u));
}

function DomainCheck() {
  const { t } = useTranslation("local-network");
  const { check } = useDnsProxy();
  const [domain, setDomain] = useState("");
  const r = check.data;
  return (
    <form
      className="grid gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        if (domain.trim()) check.mutate(domain.trim());
      }}
    >
      <FieldLabel htmlFor="ab-check">{t("dns_proxy.check_label")}</FieldLabel>
      <div className="flex gap-2">
        <Input
          id="ab-check"
          value={domain}
          onChange={(e) => {
            setDomain(e.target.value);
            check.reset();
          }}
          placeholder="ads.example.com"
          spellCheck={false}
          autoCapitalize="none"
        />
        <Button type="submit" variant="outline" disabled={!domain.trim() || check.isPending}>
          {t("dns_proxy.check_button")}
        </Button>
      </div>
      <p className="min-h-4 text-xs text-muted-foreground" aria-live="polite">
        {check.isError && (check.error instanceof Error ? check.error.message : "")}
        {r &&
          (r.blocked
            ? t("dns_proxy.check_blocked", { domain: r.domain })
            : r.allowlisted
              ? t("dns_proxy.check_allowlisted", { domain: r.domain })
              : t("dns_proxy.check_allowed", { domain: r.domain }))}
      </p>
    </form>
  );
}

function BlockingForm({ data }: { data: DnsProxyStatus }) {
  const { t, i18n } = useTranslation("local-network");
  const { save, updateLists } = useDnsProxy();
  const { saved, markSaved } = useSaveFlash();
  const [form, setForm] = useState<FormState>(() => toForm(data));
  const [advanced, setAdvanced] = useState(
    data.blocking.custom_urls.length + data.blocking.allowlist.length + data.blocking.denylist.length > 0,
  );
  const set = (p: Partial<FormState>) => setForm((f) => ({ ...f, ...p }));
  const b = data.blocking;
  const s = data.stats;

  const allowBad = badDomains(form.allowlist);
  const denyBad = badDomains(form.denylist);
  const urlBad = badURLs(form.customURLs);
  const noSource =
    form.enabled && form.lists.length === 0 && !form.customURLs.trim() && parseDomainList(form.denylist).length === 0;
  const invalid = allowBad.length > 0 || denyBad.length > 0 || urlBad.length > 0 || noSource;
  const dirty = JSON.stringify(form) !== JSON.stringify(toForm(data));

  const toggleList = (id: string, on: boolean) =>
    set({ lists: on ? [...form.lists, id] : form.lists.filter((l) => l !== id) });

  const onSave = () =>
    save.mutate(
      {
        blocking: {
          enabled: form.enabled,
          lists: form.lists,
          custom_urls: form.customURLs.split(/\s+/).filter(Boolean),
          allowlist: parseDomainList(form.allowlist),
          denylist: parseDomainList(form.denylist),
        },
      },
      {
        onSuccess: () => {
          markSaved();
          toast.success(form.enabled ? t("dns_proxy.toast_blocking_on") : t("dns_proxy.toast_blocking_off"));
        },
        onError: (e) => toast.error(e instanceof Error ? e.message : t("dns_proxy.toast_error")),
      },
    );

  const pct = s && s.queries > 0 ? Math.round((s.blocked / s.queries) * 100) : 0;
  const updated = b.updated_at
    ? new Date(b.updated_at * 1000).toLocaleDateString(i18n.language, { day: "numeric", month: "short" })
    : null;

  return (
    <div className="grid gap-5">
      <form
        className="grid gap-5"
        onSubmit={(e) => {
          e.preventDefault();
          onSave();
        }}
      >
        <FieldGroup>
          <Field orientation="horizontal" className="w-fit">
            <FieldLabel htmlFor="ad-block">{t("dns_proxy.ab_enable")}</FieldLabel>
            <Switch id="ad-block" checked={form.enabled} onCheckedChange={(v) => set({ enabled: v })} />
          </Field>

          <FieldSet>
            <FieldLegend variant="label">{t("dns_proxy.ab_lists")}</FieldLegend>
            <FieldDescription>{t("dns_proxy.ab_lists_hint")}</FieldDescription>
            <div className="grid gap-2 @md/card:grid-cols-2">
              {data.blocklists.map((l) => (
                <Field key={l.id} orientation="horizontal">
                  <Checkbox
                    id={`ab-list-${l.id}`}
                    checked={form.lists.includes(l.id)}
                    onCheckedChange={(v) => toggleList(l.id, v === true)}
                    disabled={!form.enabled}
                  />
                  <FieldLabel htmlFor={`ab-list-${l.id}`} className="font-normal">
                    {l.label}
                    {l.count > 0 && (
                      <span className="text-muted-foreground tabular-nums">
                        {" "}
                        · {l.count.toLocaleString()}
                      </span>
                    )}
                  </FieldLabel>
                </Field>
              ))}
            </div>
            {noSource && <FieldError>{t("dns_proxy.ab_no_source")}</FieldError>}
          </FieldSet>

          <Collapsible open={advanced} onOpenChange={setAdvanced}>
            <CollapsibleTrigger asChild>
              <Button type="button" variant="ghost" size="sm" className="-ml-2 w-fit text-muted-foreground">
                <ChevronDownIcon className={cn("size-4 transition-transform", advanced && "rotate-180")} />
                {t("dns_proxy.ab_advanced")}
              </Button>
            </CollapsibleTrigger>
            <CollapsibleContent className="grid gap-4 pt-3">
              <Field>
                <FieldLabel htmlFor="ab-allow">{t("dns_proxy.ab_allowlist")}</FieldLabel>
                <Textarea
                  id="ab-allow"
                  rows={3}
                  value={form.allowlist}
                  onChange={(e) => set({ allowlist: e.target.value })}
                  placeholder="example.com"
                  spellCheck={false}
                  autoCapitalize="none"
                  aria-invalid={allowBad.length > 0 || undefined}
                />
                <FieldDescription>{t("dns_proxy.ab_allowlist_hint")}</FieldDescription>
                {allowBad.length > 0 && <FieldError>{t("dns_proxy.ab_bad_domain", { domain: allowBad[0] })}</FieldError>}
              </Field>
              <Field>
                <FieldLabel htmlFor="ab-deny">{t("dns_proxy.ab_denylist")}</FieldLabel>
                <Textarea
                  id="ab-deny"
                  rows={3}
                  value={form.denylist}
                  onChange={(e) => set({ denylist: e.target.value })}
                  placeholder="tracker.example.net"
                  spellCheck={false}
                  autoCapitalize="none"
                  aria-invalid={denyBad.length > 0 || undefined}
                />
                <FieldDescription>{t("dns_proxy.ab_denylist_hint")}</FieldDescription>
                {denyBad.length > 0 && <FieldError>{t("dns_proxy.ab_bad_domain", { domain: denyBad[0] })}</FieldError>}
              </Field>
              <Field>
                <FieldLabel htmlFor="ab-urls">{t("dns_proxy.ab_custom_urls")}</FieldLabel>
                <Textarea
                  id="ab-urls"
                  rows={2}
                  value={form.customURLs}
                  onChange={(e) => set({ customURLs: e.target.value })}
                  placeholder="https://example.com/blocklist.txt"
                  spellCheck={false}
                  autoCapitalize="none"
                  aria-invalid={urlBad.length > 0 || undefined}
                />
                <FieldDescription>{t("dns_proxy.ab_custom_urls_hint")}</FieldDescription>
                {urlBad.length > 0 && <FieldError>{t("dns_proxy.ab_bad_url", { url: urlBad[0] })}</FieldError>}
              </Field>
            </CollapsibleContent>
          </Collapsible>
        </FieldGroup>

        <SaveButton
          type="submit"
          isSaving={save.isPending}
          saved={saved}
          label={t("actions.apply", { ns: "common" })}
          className="w-fit"
          disabled={!dirty || invalid}
        />
      </form>

      {b.enabled && (
        <div className="grid gap-2 rounded-lg border bg-muted/30 p-4">
          <StatRow
            label={t("dns_proxy.stat_domains")}
            value={b.updating && b.domain_count === 0 ? t("dns_proxy.stat_downloading") : b.domain_count.toLocaleString()}
          />
          {updated && <StatRow label={t("dns_proxy.stat_updated")} value={updated} />}
          {s && (
            <StatRow
              label={t("dns_proxy.stat_blocked")}
              value={t("dns_proxy.stat_blocked_value", {
                blocked: s.blocked.toLocaleString(),
                total: s.queries.toLocaleString(),
                pct,
              })}
            />
          )}
          {b.update_error && <p className="text-xs text-warning break-words">{b.update_error}</p>}
          <div className="flex flex-wrap items-center justify-between gap-2 pt-1">
            <p className="text-xs text-muted-foreground">{t("dns_proxy.stat_since_start")}</p>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() =>
                updateLists.mutate(undefined, {
                  onError: (e) => toast.error(e instanceof Error ? e.message : t("dns_proxy.toast_error")),
                })
              }
              disabled={b.updating || updateLists.isPending}
            >
              <RefreshCwIcon className={cn("size-3.5", b.updating && "animate-spin")} />
              {b.updating ? t("dns_proxy.updating") : t("dns_proxy.update_now")}
            </Button>
          </div>
          {s?.top_blocked && s.top_blocked.length > 0 && (
            <div className="grid gap-1 border-t pt-3">
              <p className="text-xs font-medium text-muted-foreground">{t("dns_proxy.top_blocked")}</p>
              <ul className="grid gap-0.5">
                {s.top_blocked.slice(0, 5).map((n) => (
                  <li key={n.name} className="flex justify-between gap-4 text-xs">
                    <span className="truncate">{n.name}</span>
                    <span className="tabular-nums text-muted-foreground">{n.count.toLocaleString()}</span>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}

      {b.enabled && <DomainCheck />}

      <p className="text-xs text-muted-foreground">{t("dns_proxy.ab_note")}</p>
    </div>
  );
}

export default function AdBlockingCard() {
  const { t } = useTranslation("local-network");
  const { data, isLoading } = useDnsProxy();
  const badge = data ? blockingBadge(data, t) : null;
  const seed = data
    ? JSON.stringify([data.blocking.enabled, data.blocking.lists, data.blocking.custom_urls, data.blocking.allowlist, data.blocking.denylist])
    : "";
  return (
    <Card className="@container/card">
      <CardHeader>
        <CardTitle>{t("dns_proxy.ab_title")}</CardTitle>
        <CardDescription>{t("dns_proxy.ab_description")}</CardDescription>
        {badge && (
          <CardAction>
            <DnsProxyBadge state={badge.state} label={badge.label} />
          </CardAction>
        )}
      </CardHeader>
      <CardContent>
        {isLoading || !data ? (
          <div className="grid gap-4">
            <Skeleton className="h-8 w-48" />
            <Skeleton className="h-24 w-full" />
            <Skeleton className="h-10 w-full" />
          </div>
        ) : (
          <BlockingForm key={seed} data={data} />
        )}
      </CardContent>
    </Card>
  );
}
