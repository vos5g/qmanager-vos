"use client";

import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
  AlertTriangleIcon,
  ChevronDownIcon,
  CopyIcon,
  FileUpIcon,
  KeyRoundIcon,
  PlusIcon,
  Trash2Icon,
} from "lucide-react";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
  FieldLegend,
  FieldSet,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { SaveButton, useSaveFlash } from "@/components/ui/save-button";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import {
  DEFAULT_OPTIONS,
  emptyForm,
  emptyPeer,
  type WgActionResult,
  type WgConfigForm,
  type WgOptions,
  type WgPeerForm,
} from "@/hooks/use-wireguard";
import { WireGuardOptions } from "./wireguard-options";

export async function copyText(text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    document.execCommand("copy");
    document.body.removeChild(ta);
  }
}

/** Turns "Mullvad gb-lon 1.conf" into a name the stick accepts. */
function nameFromFile(file: string): string {
  return file
    .replace(/\.conf$/i, "")
    .replace(/[^A-Za-z0-9 ._-]/g, "-")
    .replace(/\.{2,}/g, ".")
    .replace(/^[\s._-]+|[\s._-]+$/g, "")
    .slice(0, 32)
    .trim();
}

type Tab = "upload" | "paste" | "manual";

interface WireGuardConfigDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Name of the config to edit; undefined adds a new one. */
  editName?: string;
  kernelAvailable: boolean;
  getConfig: (name: string) => Promise<WgConfigForm | null>;
  saveConfig: (form: WgConfigForm, originalName?: string) => Promise<WgActionResult>;
  importConfig: (name: string, text: string, opts: WgOptions) => Promise<WgActionResult>;
}

export function WireGuardConfigDialog({
  open,
  onOpenChange,
  editName,
  kernelAvailable,
  getConfig,
  saveConfig,
  importConfig,
}: WireGuardConfigDialogProps) {
  const { t } = useTranslation("monitoring");
  const editing = editName !== undefined;
  const [tab, setTab] = useState<Tab>("upload");
  const [form, setForm] = useState<WgConfigForm>(emptyForm);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const { saved, markSaved } = useSaveFlash(900);
  // import tabs
  const [importName, setImportName] = useState("");
  const [fileText, setFileText] = useState("");
  const [fileLabel, setFileLabel] = useState("");
  const [pasteText, setPasteText] = useState("");
  const [importOpts, setImportOpts] = useState<WgOptions>(DEFAULT_OPTIONS);
  const fileInput = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!open) return;
    setErr(null);
    setSaving(false);
    if (editing) {
      setLoading(true);
      void getConfig(editName).then((f) => {
        if (f) setForm(f);
        else setErr(t("wireguard.toast_failed"));
        setLoading(false);
      });
    } else {
      // The kernel engine is much faster for devices behind the stick; use it
      // by default when the module is installed.
      const engine = kernelAvailable ? "kernel" : "userspace";
      setTab("upload");
      setForm({ ...emptyForm(), engine });
      setImportName("");
      setFileText("");
      setFileLabel("");
      setPasteText("");
      setImportOpts({ ...DEFAULT_OPTIONS, engine });
    }
  }, [open, editing, editName, getConfig, t, kernelAvailable]);

  const set = <K extends keyof WgConfigForm>(k: K, v: WgConfigForm[K]) =>
    setForm((f) => ({ ...f, [k]: v }));
  const setPeer = (i: number, patch: Partial<WgPeerForm>) =>
    setForm((f) => ({ ...f, peers: f.peers.map((p, j) => (j === i ? { ...p, ...patch } : p)) }));

  const onFile = async (file: File | undefined) => {
    if (!file) return;
    if (file.size > 16 * 1024) {
      setErr(t("wireguard.upload_hint"));
      return;
    }
    const text = await file.text();
    setFileText(text);
    setFileLabel(t("wireguard.upload_loaded", { file: file.name, lines: text.split("\n").length }));
    if (!importName) setImportName(nameFromFile(file.name));
    setErr(null);
  };

  const done = (res: WgActionResult, verb: "toast_saved" | "toast_imported", showKey: boolean) => {
    markSaved();
    const name = res.saved ?? "";
    toast.success(t(`wireguard.${verb}`, { name }));
    if (res.ignored && res.ignored.length > 0) {
      toast.warning(t("wireguard.toast_ignored", { lines: res.ignored.join("; ") }), {
        description: t("wireguard.toast_ignored_hint"),
      });
    }
    if (res.warning) toast.warning(res.warning);
    if (showKey && res.public_key) {
      const key = res.public_key;
      toast.info(t("wireguard.toast_public_key"), {
        description: key,
        duration: 20000,
        action: {
          label: t("wireguard.copy_public_key"),
          onClick: () => void copyText(key).then(() => toast.success(t("wireguard.toast_copied"))),
        },
      });
    }
    setTimeout(() => onOpenChange(false), 350);
  };

  const submit = async () => {
    setErr(null);
    setSaving(true);
    try {
      if (!editing && tab !== "manual") {
        const text = tab === "upload" ? fileText : pasteText;
        const res = await importConfig(importName, text, importOpts);
        if (!res.success) setErr(res.detail || res.error || t("wireguard.toast_failed"));
        else done(res, "toast_imported", false);
        return;
      }
      const res = await saveConfig(form, editName);
      if (!res.success) setErr(res.detail || res.error || t("wireguard.toast_failed"));
      else done(res, editing ? "toast_saved" : "toast_imported", form.generate_key);
    } finally {
      setSaving(false);
    }
  };

  const canSubmit = editing || tab === "manual"
    ? form.name.trim() !== ""
    : importName.trim() !== "" && (tab === "upload" ? fileText !== "" : pasteText.trim() !== "");

  const nameField = (value: string, onChange: (v: string) => void, idSuffix: string) => (
    <Field>
      <FieldLabel htmlFor={`wg-name-${idSuffix}`}>{t("wireguard.name_label")}</FieldLabel>
      <Input
        id={`wg-name-${idSuffix}`}
        value={value}
        maxLength={32}
        placeholder={t("wireguard.name_placeholder")}
        onChange={(e) => onChange(e.target.value)}
        autoComplete="off"
      />
      <FieldDescription>{t("wireguard.name_hint")}</FieldDescription>
    </Field>
  );

  const manualForm = (
    <div className="grid gap-6">
      {nameField(form.name, (v) => set("name", v), "manual")}

      <FieldSet>
        <FieldLegend>{t("wireguard.section_interface")}</FieldLegend>
        <FieldGroup className="gap-5">
          <Field>
            <FieldLabel htmlFor="wg-private-key">{t("wireguard.private_key_label")}</FieldLabel>
            {form.generate_key ? (
              <div className="flex flex-wrap items-center gap-2 rounded-md border border-dashed p-3 text-sm text-muted-foreground">
                <KeyRoundIcon className="size-4 shrink-0" />
                <span className="flex-1">{t("wireguard.private_key_generating")}</span>
                <Button type="button" variant="ghost" size="sm" onClick={() => set("generate_key", false)}>
                  {t("wireguard.private_key_use_own")}
                </Button>
              </div>
            ) : (
              <div className="flex flex-col gap-2 @md/dialog:flex-row">
                <Input
                  id="wg-private-key"
                  type="password"
                  autoComplete="off"
                  spellCheck={false}
                  className="font-mono text-xs"
                  value={form.private_key}
                  placeholder={
                    form.has_private_key
                      ? t("wireguard.private_key_placeholder_keep")
                      : t("wireguard.private_key_placeholder_new")
                  }
                  onChange={(e) => set("private_key", e.target.value)}
                />
                <Button
                  type="button"
                  variant="secondary"
                  className="shrink-0"
                  onClick={() => setForm((f) => ({ ...f, generate_key: true, private_key: "" }))}
                >
                  <KeyRoundIcon className="size-4" />
                  {t("wireguard.private_key_generate")}
                </Button>
              </div>
            )}
          </Field>
          {form.public_key && !form.generate_key && form.private_key === "" && (
            <Field>
              <FieldLabel>{t("wireguard.public_key_label")}</FieldLabel>
              <div className="flex items-center gap-2">
                <code className="min-w-0 flex-1 truncate rounded-md bg-muted px-3 py-2 font-mono text-xs">
                  {form.public_key}
                </code>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  aria-label={t("wireguard.copy_public_key")}
                  onClick={() =>
                    void copyText(form.public_key).then(() => toast.success(t("wireguard.toast_copied")))
                  }
                >
                  <CopyIcon className="size-4" />
                </Button>
              </div>
              <FieldDescription>{t("wireguard.public_key_hint")}</FieldDescription>
            </Field>
          )}
          <div className="grid gap-5 @md/dialog:grid-cols-2">
            <Field>
              <FieldLabel htmlFor="wg-address">{t("wireguard.addresses_label")}</FieldLabel>
              <Input
                id="wg-address"
                value={form.addresses}
                placeholder="10.64.12.34/32"
                className="tabular-nums"
                onChange={(e) => set("addresses", e.target.value)}
              />
              <FieldDescription>{t("wireguard.addresses_hint")}</FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor="wg-dns">{t("wireguard.dns_label")}</FieldLabel>
              <Input
                id="wg-dns"
                value={form.dns}
                placeholder="10.64.0.1"
                className="tabular-nums"
                onChange={(e) => set("dns", e.target.value)}
              />
              <FieldDescription>{t("wireguard.dns_hint")}</FieldDescription>
            </Field>
          </div>
        </FieldGroup>
      </FieldSet>

      {form.peers.map((p, i) => (
        <FieldSet key={i}>
          <div className="flex items-center justify-between gap-2">
            <FieldLegend className="mb-0">
              {form.peers.length > 1 ? t("wireguard.section_peer_n", { n: i + 1 }) : t("wireguard.section_peer")}
            </FieldLegend>
            {form.peers.length > 1 && (
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="text-muted-foreground"
                onClick={() => setForm((f) => ({ ...f, peers: f.peers.filter((_, j) => j !== i) }))}
              >
                <Trash2Icon className="size-4" />
                {t("wireguard.remove_peer")}
              </Button>
            )}
          </div>
          <FieldGroup className="gap-5">
            <Field>
              <FieldLabel htmlFor={`wg-peer-pub-${i}`}>{t("wireguard.peer_public_key_label")}</FieldLabel>
              <Input
                id={`wg-peer-pub-${i}`}
                value={p.public_key}
                spellCheck={false}
                autoComplete="off"
                className="font-mono text-xs"
                onChange={(e) => setPeer(i, { public_key: e.target.value })}
              />
            </Field>
            <div className="grid gap-5 @md/dialog:grid-cols-2">
              <Field>
                <FieldLabel htmlFor={`wg-peer-ep-${i}`}>{t("wireguard.endpoint_label")}</FieldLabel>
                <Input
                  id={`wg-peer-ep-${i}`}
                  value={p.endpoint}
                  placeholder="vpn.example.com:51820"
                  autoComplete="off"
                  onChange={(e) => setPeer(i, { endpoint: e.target.value })}
                />
                <FieldDescription>{t("wireguard.endpoint_hint")}</FieldDescription>
              </Field>
              <Field>
                <FieldLabel htmlFor={`wg-peer-allowed-${i}`}>{t("wireguard.allowed_ips_label")}</FieldLabel>
                <Input
                  id={`wg-peer-allowed-${i}`}
                  value={p.allowed_ips}
                  className="tabular-nums"
                  onChange={(e) => setPeer(i, { allowed_ips: e.target.value })}
                />
                <FieldDescription>{t("wireguard.allowed_ips_hint")}</FieldDescription>
              </Field>
            </div>
            <div className="grid gap-5 @md/dialog:grid-cols-[1fr_9rem]">
              <Field>
                <FieldLabel htmlFor={`wg-peer-psk-${i}`}>{t("wireguard.psk_label")}</FieldLabel>
                <Input
                  id={`wg-peer-psk-${i}`}
                  type="password"
                  autoComplete="off"
                  spellCheck={false}
                  className="font-mono text-xs"
                  value={p.preshared_key}
                  disabled={p.clear_preshared_key}
                  placeholder={
                    p.has_preshared_key ? t("wireguard.psk_placeholder_keep") : t("wireguard.psk_placeholder_new")
                  }
                  onChange={(e) => setPeer(i, { preshared_key: e.target.value })}
                />
                {p.has_preshared_key && (
                  <Field orientation="horizontal" className="mt-1">
                    <Checkbox
                      id={`wg-peer-psk-clear-${i}`}
                      checked={p.clear_preshared_key}
                      onCheckedChange={(v) => setPeer(i, { clear_preshared_key: v === true, preshared_key: "" })}
                    />
                    <FieldLabel htmlFor={`wg-peer-psk-clear-${i}`} className="font-normal">
                      {t("wireguard.psk_clear")}
                    </FieldLabel>
                  </Field>
                )}
              </Field>
              <Field>
                <FieldLabel htmlFor={`wg-peer-ka-${i}`}>{t("wireguard.keepalive_label")}</FieldLabel>
                <Input
                  id={`wg-peer-ka-${i}`}
                  inputMode="numeric"
                  className="tabular-nums"
                  value={p.keepalive}
                  onChange={(e) => setPeer(i, { keepalive: e.target.value.replace(/\D/g, "") })}
                />
              </Field>
            </div>
            <FieldDescription className="-mt-2">{t("wireguard.keepalive_hint")}</FieldDescription>
          </FieldGroup>
        </FieldSet>
      ))}
      {form.peers.length < 16 && (
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="w-fit"
          onClick={() => setForm((f) => ({ ...f, peers: [...f.peers, emptyPeer()] }))}
        >
          <PlusIcon className="size-4" />
          {t("wireguard.add_peer")}
        </Button>
      )}

      <Collapsible>
        <CollapsibleTrigger asChild>
          <Button type="button" variant="ghost" size="sm" className="group w-fit text-muted-foreground">
            <ChevronDownIcon className="size-4 transition-transform group-data-[state=open]:rotate-180" />
            {t("wireguard.advanced")}
          </Button>
        </CollapsibleTrigger>
        <CollapsibleContent className="pt-3">
          <div className="grid gap-5 @md/dialog:grid-cols-2">
            <Field>
              <FieldLabel htmlFor="wg-mtu">{t("wireguard.mtu_label")}</FieldLabel>
              <Input
                id="wg-mtu"
                inputMode="numeric"
                className="tabular-nums"
                placeholder="1420"
                value={form.mtu}
                onChange={(e) => set("mtu", e.target.value.replace(/\D/g, ""))}
              />
              <FieldDescription>{t("wireguard.mtu_hint")}</FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor="wg-listen">{t("wireguard.listen_port_label")}</FieldLabel>
              <Input
                id="wg-listen"
                inputMode="numeric"
                className="tabular-nums"
                value={form.listen_port}
                onChange={(e) => set("listen_port", e.target.value.replace(/\D/g, ""))}
              />
              <FieldDescription>{t("wireguard.listen_port_hint")}</FieldDescription>
            </Field>
          </div>
        </CollapsibleContent>
      </Collapsible>

      <Separator />
      <FieldSet>
        <FieldLegend>{t("wireguard.section_options")}</FieldLegend>
        <WireGuardOptions
          idPrefix="wg-form"
          value={form}
          onChange={(o) => setForm((f) => ({ ...f, ...o }))}
          kernelAvailable={kernelAvailable}
        />
      </FieldSet>
    </div>
  );

  const importOptions = (
    <>
      <Separator />
      <FieldSet>
        <FieldLegend>{t("wireguard.section_options")}</FieldLegend>
        <WireGuardOptions
          idPrefix={`wg-import-${tab}`}
          value={importOpts}
          onChange={setImportOpts}
          kernelAvailable={kernelAvailable}
        />
      </FieldSet>
    </>
  );

  return (
    <Dialog open={open} onOpenChange={(o) => !saving && onOpenChange(o)}>
      <DialogContent className="flex max-h-[90dvh] flex-col gap-0 p-0 sm:max-w-2xl">
        <DialogHeader className="border-b px-6 pt-6 pb-4">
          <DialogTitle>
            {editing ? t("wireguard.dialog_edit_title", { name: editName }) : t("wireguard.dialog_add_title")}
          </DialogTitle>
          <DialogDescription>
            {editing ? t("wireguard.dialog_edit_description") : t("wireguard.dialog_add_description")}
          </DialogDescription>
        </DialogHeader>

        <div className="@container/dialog min-h-0 flex-1 overflow-y-auto px-6 py-5">
          {err && (
            <Alert variant="destructive" className="mb-5">
              <AlertTriangleIcon className="size-4" />
              <AlertDescription>{err}</AlertDescription>
            </Alert>
          )}
          {editing ? (
            loading ? (
              <div className="grid gap-4">
                <Skeleton className="h-16 w-full" />
                <Skeleton className="h-32 w-full" />
                <Skeleton className="h-32 w-full" />
              </div>
            ) : (
              manualForm
            )
          ) : (
            <Tabs value={tab} onValueChange={(v) => { setTab(v as Tab); setErr(null); }} className="gap-5">
              <TabsList className="w-full">
                <TabsTrigger value="upload">{t("wireguard.tab_upload")}</TabsTrigger>
                <TabsTrigger value="paste">{t("wireguard.tab_paste")}</TabsTrigger>
                <TabsTrigger value="manual">{t("wireguard.tab_manual")}</TabsTrigger>
              </TabsList>

              <TabsContent value="upload" className="grid gap-6">
                <Field>
                  <FieldLabel>{t("wireguard.upload_label")}</FieldLabel>
                  <input
                    ref={fileInput}
                    type="file"
                    accept=".conf,text/plain"
                    className="sr-only"
                    onChange={(e) => {
                      void onFile(e.target.files?.[0]);
                      e.target.value = "";
                    }}
                  />
                  <Button
                    type="button"
                    variant="outline"
                    className="h-auto justify-start gap-3 border-dashed py-4"
                    onClick={() => fileInput.current?.click()}
                  >
                    <FileUpIcon className="size-5 text-muted-foreground" />
                    <span className="min-w-0 truncate text-left">
                      {fileLabel || t("wireguard.upload_choose")}
                    </span>
                  </Button>
                  <FieldDescription>{t("wireguard.upload_hint")}</FieldDescription>
                </Field>
                {nameField(importName, setImportName, "upload")}
                {importOptions}
              </TabsContent>

              <TabsContent value="paste" className="grid gap-6">
                <Field>
                  <FieldLabel htmlFor="wg-paste">{t("wireguard.paste_label")}</FieldLabel>
                  <Textarea
                    id="wg-paste"
                    value={pasteText}
                    rows={10}
                    spellCheck={false}
                    autoComplete="off"
                    className="font-mono text-xs"
                    placeholder={t("wireguard.paste_placeholder")}
                    onChange={(e) => setPasteText(e.target.value)}
                  />
                </Field>
                {nameField(importName, setImportName, "paste")}
                {importOptions}
              </TabsContent>

              <TabsContent value="manual">{manualForm}</TabsContent>
            </Tabs>
          )}
        </div>

        <DialogFooter className="border-t px-6 py-4">
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={saving}>
            {t("wireguard.cancel")}
          </Button>
          <SaveButton
            isSaving={saving}
            saved={saved}
            disabled={!canSubmit || loading}
            onClick={() => void submit()}
            label={editing ? t("wireguard.save") : t("wireguard.add")}
            savingLabel={editing ? t("wireguard.saving") : t("wireguard.adding")}
            savedLabel={t("wireguard.saved")}
          />
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
