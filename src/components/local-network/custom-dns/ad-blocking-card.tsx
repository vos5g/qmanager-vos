"use client";

import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChevronDownIcon, PlayIcon, RefreshCwIcon } from "lucide-react";
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
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { parseDomainList, useDnsProxy, type DnsHistory, type DnsProxyStatus } from "@/hooks/use-dns-proxy";
import { DnsProxyBadge, StatRow, type DnsProxyBadgeState } from "./dns-proxy-badge";

const DOMAIN_RE = /^(\*\.)?([a-z0-9_-]{1,63}\.)+[a-z0-9-]*[a-z][a-z0-9-]*\.?$/i;

function blockingBadge(d: DnsProxyStatus, t: (k: string) => string): { state: DnsProxyBadgeState; label?: string } {
  const b = d.blocking;
  if (!b.enabled) return { state: "off" };
  if (!d.running || !d.redirect_active) return { state: "warning", label: t("dns_proxy.status_not_running") };
  if (b.paused_until !== 0) return { state: "warning", label: t("dns_proxy.status_paused") };
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

function percent(blocked: number, queries: number) {
  return queries > 0 ? Math.round((blocked / queries) * 100) : 0;
}

function PauseControl({ data }: { data: DnsProxyStatus }) {
  const { t } = useTranslation("local-network");
  const { pause, resume } = useDnsProxy();
  const until = data.blocking.paused_until;
  const onError = (e: unknown) => toast.error(e instanceof Error ? e.message : t("dns_proxy.toast_error"));
  if (until !== 0) {
    return (
      <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-warning/30 bg-warning/10 px-4 py-3">
        <p className="text-sm">
          {until === -1
            ? t("dns_proxy.paused_indefinitely")
            : t("dns_proxy.paused_until", {
                time: new Date(until * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }),
              })}
        </p>
        <Button type="button" size="sm" onClick={() => resume.mutate(undefined, { onError })} disabled={resume.isPending}>
          <PlayIcon className="size-3.5" />
          {t("dns_proxy.resume")}
        </Button>
      </div>
    );
  }
  return (
    <div className="flex flex-wrap items-center gap-2">
      <span className="text-sm text-muted-foreground">{t("dns_proxy.pause_label")}</span>
      {[
        { minutes: 5, label: t("dns_proxy.pause_5m") },
        { minutes: 60, label: t("dns_proxy.pause_1h") },
        { minutes: 0, label: t("dns_proxy.pause_until_resumed") },
      ].map((o) => (
        <Button
          key={o.minutes}
          type="button"
          variant="outline"
          size="sm"
          onClick={() => pause.mutate(o.minutes, { onError })}
          disabled={pause.isPending}
        >
          {o.label}
        </Button>
      ))}
    </div>
  );
}

function BlockedHistory({ history }: { history: DnsHistory }) {
  const { t, i18n } = useTranslation("local-network");
  const { resetStats } = useDnsProxy();
  const max = Math.max(1, ...history.days.map((d) => d.queries));
  const fmt = (iso: string) =>
    new Date(`${iso}T00:00:00`).toLocaleDateString(i18n.language, { day: "numeric", month: "short" });
  const today = history.today;
  return (
    <div className="grid gap-3 rounded-lg border p-4">
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <p className="text-sm font-medium">{t("dns_proxy.history_title")}</p>
        <p className="text-sm tabular-nums text-muted-foreground">
          {t("dns_proxy.history_today", {
            blocked: today.blocked.toLocaleString(),
            total: today.queries.toLocaleString(),
            pct: percent(today.blocked, today.queries),
          })}
        </p>
      </div>
      <div className="grid gap-1.5">
        <div className="flex h-20 items-end gap-[3px]" role="img" aria-label={t("dns_proxy.history_aria")}>
          {history.days.map((d) => (
            <Tooltip key={d.date}>
              <TooltipTrigger asChild>
                <div className="relative flex h-full flex-1 items-end">
                  <div
                    className={cn("w-full rounded-sm", d.queries > 0 ? "bg-muted-foreground/20" : "bg-muted")}
                    style={{ height: `${d.queries > 0 ? Math.max(4, (d.queries / max) * 100) : 4}%` }}
                  >
                    {d.blocked > 0 && (
                      <div
                        className="absolute inset-x-0 bottom-0 rounded-sm bg-primary/70"
                        style={{ height: `${Math.max(3, (d.blocked / max) * 100)}%` }}
                      />
                    )}
                  </div>
                </div>
              </TooltipTrigger>
              <TooltipContent>
                <p className="tabular-nums">
                  {t("dns_proxy.history_tooltip", {
                    date: fmt(d.date),
                    blocked: d.blocked.toLocaleString(),
                    total: d.queries.toLocaleString(),
                  })}
                </p>
              </TooltipContent>
            </Tooltip>
          ))}
        </div>
        <div className="flex justify-between text-[10px] text-muted-foreground">
          <span>{history.days.length ? fmt(history.days[0].date) : ""}</span>
          <span>{t("dns_proxy.history_today_label")}</span>
        </div>
        <div className="flex gap-4 text-xs text-muted-foreground">
          <span className="inline-flex items-center gap-1.5">
            <span className="size-2 rounded-sm bg-primary/70" aria-hidden />
            {t("dns_proxy.legend_blocked")}
          </span>
          <span className="inline-flex items-center gap-1.5">
            <span className="size-2 rounded-sm bg-muted-foreground/20" aria-hidden />
            {t("dns_proxy.legend_lookups")}
          </span>
        </div>
      </div>
      {history.top.length > 0 && (
        <div className="grid gap-1 border-t pt-3">
          <p className="text-xs font-medium text-muted-foreground">{t("dns_proxy.top_blocked")}</p>
          <ul className="grid gap-0.5">
            {history.top.slice(0, 5).map((n) => (
              <li key={n.name} className="flex justify-between gap-4 text-xs">
                <span className="truncate">{n.name}</span>
                <span className="tabular-nums text-muted-foreground">{n.count.toLocaleString()}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
      <div className="flex flex-wrap items-center justify-between gap-2 border-t pt-3">
        <p className="text-xs text-muted-foreground">
          {t("dns_proxy.history_since", {
            date: new Date(history.since * 1000).toLocaleDateString(i18n.language, { day: "numeric", month: "short" }),
            blocked: history.total.blocked.toLocaleString(),
          })}
        </p>
        <AlertDialog>
          <AlertDialogTrigger asChild>
            <Button type="button" variant="ghost" size="sm" className="text-muted-foreground">
              {t("dns_proxy.reset_stats")}
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{t("dns_proxy.reset_stats_title")}</AlertDialogTitle>
              <AlertDialogDescription>{t("dns_proxy.reset_stats_description")}</AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>{t("actions.cancel", { ns: "common" })}</AlertDialogCancel>
              <AlertDialogAction onClick={() => resetStats.mutate()}>{t("dns_proxy.reset_stats")}</AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </div>
    </div>
  );
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

      {b.enabled && <PauseControl data={data} />}

      {b.enabled && (
        <div className="grid gap-2 rounded-lg border bg-muted/30 p-4">
          <StatRow
            label={t("dns_proxy.stat_domains")}
            value={b.updating && b.domain_count === 0 ? t("dns_proxy.stat_downloading") : b.domain_count.toLocaleString()}
          />
          {updated && <StatRow label={t("dns_proxy.stat_updated")} value={updated} />}
          {b.update_error && <p className="text-xs text-warning break-words">{b.update_error}</p>}
          <div className="flex justify-end pt-1">
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
        </div>
      )}

      {b.enabled && data.history && <BlockedHistory history={data.history} />}
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
