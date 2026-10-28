"use client";

import { useEffect, useState } from "react";
import { Stethoscope } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Alert, AlertDescription } from "@/components/ui/alert";

type Entry = { path: string; type: string; size: number };
type FileData = {
  path: string;
  bytes: number;
  sha256: string;
  text: string;
  base64: string;
};
const endpoint = "/cgi-bin/quecmanager/system/modem_efs.sh";
export default function ModemDiagnostics() {
  const [status, setStatus] = useState<{
    installed: boolean;
    available: boolean;
  } | null>(null);
  const [path, setPath] = useState("/policyman");
  const [listed, setListed] = useState("");
  const [entries, setEntries] = useState<Entry[]>([]);
  const [file, setFile] = useState<FileData | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    fetch(`${endpoint}?action=status`)
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        return r.json();
      })
      .then((d) => {
        if (!d.success) throw new Error(d.detail || "Unable to check helper");
        setStatus(d);
      })
      .catch((e) => setError(e.message));
  }, []);
  async function browse(target: string, action: "list" | "read") {
    setBusy(true);
    setError("");
    setFile(null);
    try {
      const r = await fetch(
        `${endpoint}?${new URLSearchParams({ action, path: target })}`,
        { cache: "no-store" },
      );
      const d = await r.json();
      if (!r.ok || !d.success) throw new Error(d.detail || "EFS read failed");
      if (action === "list") {
        setEntries(d.entries);
        setListed(target);
        setPath(target);
      } else {
        setFile(d);
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : "EFS read failed");
    } finally {
      setBusy(false);
    }
  }
  function download() {
    if (!file) return;
    const data = Uint8Array.from(atob(file.base64), (ch) => ch.charCodeAt(0));
    const url = URL.createObjectURL(new Blob([data]));
    const a = document.createElement("a");
    a.href = url;
    a.download = file.path.split("/").pop() || "modem-efs.bin";
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
  return (
    <div className="space-y-6">
      <PageHeader icon={Stethoscope} title="Modem diagnostics" />
      <Card>
        <CardHeader>
          <CardTitle>Qualcomm DIAG / EFS reader</CardTitle>
          <CardDescription>
            Read the modem’s internal storage from the stick’s ARM processor.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <p className="text-sm text-muted-foreground">
            The local ARM helper accesses /dev/diag without changing USB mode.
            Browse PolicyMan and modem capability files, including
            carrier-aggregation policy files when the firmware exposes them. XML
            and text are shown directly; binary formats can be downloaded for
            analysis. A policy file alone does not prove which combinations the
            radio can use.
          </p>
          {status && (
            <p className="text-sm">
              ARM helper: {status.installed ? "Installed" : "Not installed"} ·
              Modem DIAG: {status.available ? "Available" : "Unavailable"}
            </p>
          )}
          {status && !status.installed && (
            <Alert>
              <AlertDescription>
                Build tools/build-diag-bridge.sh in vos5g-sdx55, then install
                with install.sh --diag-bridge dist/vos5g-diag-bridge-armv7. The
                helper runs only during an operation. Close other DIAG sessions
                first.
              </AlertDescription>
            </Alert>
          )}
          <div className="flex flex-wrap gap-2">
            <Button
              variant="outline"
              disabled={busy || !status?.installed || !status.available}
              onClick={() => void browse("/policyman", "list")}
            >
              Band policies
            </Button>
            <Button
              variant="outline"
              disabled={busy || !status?.installed || !status.available}
              onClick={() => void browse("/nv/item_files/modem", "list")}
            >
              Modem capabilities
            </Button>
          </div>
          <form
            className="flex gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              void browse(path, "list");
            }}
          >
            <Input
              aria-label="EFS directory"
              value={path}
              onChange={(e) => setPath(e.target.value)}
            />
            <Button disabled={busy || !status?.installed || !status.available}>
              {busy ? "Reading…" : "Browse"}
            </Button>
          </form>
          <p className="text-xs text-muted-foreground">
            Read-only access under /policyman and /nv/item_files/modem.
            Downloads can contain private device information; keep them private.
          </p>
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          {listed && (
            <div className="space-y-2">
              <div className="flex items-center gap-2">
                <span className="text-sm font-medium break-all">{listed}</span>
                {!["/policyman", "/nv/item_files/modem"].includes(listed) && (
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={busy}
                    onClick={() =>
                      void browse(
                        listed.slice(0, listed.lastIndexOf("/")),
                        "list",
                      )
                    }
                  >
                    Up
                  </Button>
                )}
              </div>
              {entries.length === 0 && (
                <p className="text-sm text-muted-foreground">
                  This directory is empty.
                </p>
              )}
              <div className="divide-y">
                {entries.map((entry) => (
                  <div
                    key={entry.path}
                    className="flex items-center justify-between gap-2 py-2"
                  >
                    <Button
                      variant="link"
                      className="h-auto whitespace-normal break-all text-left"
                      disabled={
                        busy ||
                        !["file", "item_file", "directory"].includes(entry.type)
                      }
                      onClick={() =>
                        void browse(
                          entry.path,
                          entry.type === "directory" ? "list" : "read",
                        )
                      }
                    >
                      {entry.path.split("/").pop()}
                      {entry.type === "directory" ? "/" : ""}
                    </Button>
                    <span className="text-xs text-muted-foreground shrink-0">
                      {["file", "item_file"].includes(entry.type)
                        ? `${entry.size} bytes`
                        : entry.type}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          )}
          {file && (
            <div className="space-y-2 border-t pt-4">
              <p className="text-sm font-medium break-all">
                {file.path} · {file.bytes} bytes
              </p>
              <p className="text-xs text-muted-foreground break-all">
                SHA-256: {file.sha256}
              </p>
              <Button variant="outline" onClick={download}>
                Download file
              </Button>
              <pre className="max-h-96 overflow-auto rounded-md bg-muted p-3 text-xs whitespace-pre-wrap break-all">
                {file.text ||
                  (file.bytes
                    ? "Binary file — download for analysis."
                    : "Empty file.")}
              </pre>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
