"use client";

import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { rebootNow } from "@/lib/reboot";

type Mode = { pid: string; label: string; description: string };
// Mirror the backend restriction, including when an older response is cached.
const SAFE_USB_PIDS = new Set(["9063", "9064", "9165"]);
const endpoint = "/cgi-bin/quecmanager/system/usb_mode.sh";
export function USBModeCard() {
  const [modes, setModes] = useState<Mode[]>([]);
  const [selected, setSelected] = useState("");
  const [next, setNext] = useState("");
  const [current, setCurrent] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const [saved, setSaved] = useState(false);
  const load = useCallback(async () => {
    try {
      const r = await fetch(endpoint, { cache: "no-store" });
      const d = await r.json();
      if (!r.ok || !d.success)
        throw new Error(d.detail || "Unable to read USB mode");
      setModes(d.options.filter((mode: Mode) => SAFE_USB_PIDS.has(mode.pid)));
      setNext(d.next_pid);
      setSelected(SAFE_USB_PIDS.has(d.next_pid) ? d.next_pid : "");
      // Display only a validated USB identifier, never raw modem output.
      const pid = typeof d.current_pid === "string" && /^[0-9a-f]{4}$/i.test(d.current_pid)
        ? d.current_pid.toUpperCase() : "";
      setCurrent(pid ? d.options.find((mode: Mode) => mode.pid === pid)?.label || `USB composition (${pid})` : "Unavailable");
    } catch (e) {
      setError(e instanceof Error ? e.message : "Unable to read USB mode");
    }
  }, []);
  useEffect(() => {
    const id = setTimeout(() => void load(), 0);
    return () => clearTimeout(id);
  }, [load]);
  async function save() {
    setBusy(true);
    setError("");
    try {
      const r = await fetch(endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ pid: selected, confirm: true }),
      });
      const d = await r.json();
      if (!r.ok || !d.success)
        throw new Error(d.detail || "Unable to save USB mode");
      setNext(d.next_pid);
      setSaved(true);
      toast.success("USB mode saved for the next reboot");
    } catch (e) {
      setError(e instanceof Error ? e.message : "Unable to save USB mode");
    } finally {
      setBusy(false);
    }
  }
  const description = modes.find((m) => m.pid === selected)?.description;
  return (
    <Card>
      <CardHeader>
        <CardTitle>USB mode</CardTitle>
        <CardDescription>
          Choose how the modem appears to its USB host after the next reboot.
          Only modes retaining USB networking for SSH are available.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <p className="text-sm text-muted-foreground">
          Current: {current || "Unavailable"}
          <br />
          Next boot: {next || "Unavailable"}
        </p>
        <Select
          value={selected}
          onValueChange={setSelected}
          disabled={busy || !modes.length}
        >
          <SelectTrigger aria-label="USB mode">
            <SelectValue placeholder="Select USB mode" />
          </SelectTrigger>
          <SelectContent>
            {modes.map((m) => (
              <SelectItem key={m.pid} value={m.pid}>
                {m.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p className="text-sm text-muted-foreground">{description}</p>
        {next && !SAFE_USB_PIDS.has(next) && (
          <Alert variant="destructive"><AlertDescription>
            The saved USB mode is blocked because it can remove USB networking.
            Choose and save one of the available modes before rebooting.
          </AlertDescription></Alert>
        )}
        {error && (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <div className="flex flex-wrap gap-2">
          <Button
            disabled={busy || !SAFE_USB_PIDS.has(selected) || selected === next}
            onClick={() => setConfirm(true)}
          >
            {busy ? "Saving…" : "Save USB mode"}
          </Button>
          <Button
            variant="outline"
            disabled={busy || !SAFE_USB_PIDS.has(next) || selected !== next}
            onClick={() => setConfirm(true)}
          >
            {saved ? "Reboot to activate" : "Review and reboot"}
          </Button>
        </div>
        <AlertDialog open={confirm} onOpenChange={setConfirm}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>
                {selected === next
                  ? "Reboot with this USB mode?"
                  : "Save this USB mode?"}
              </AlertDialogTitle>
              <AlertDialogDescription>
                {description} This mode retains USB networking for SSH. Rebooting
                briefly interrupts the connection while the modem restarts.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction
                onClick={() => {
                  setConfirm(false);
                  if (selected === next) rebootNow("manual");
                  else void save();
                }}
              >
                {selected === next ? "Reboot now" : "Save for next reboot"}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </CardContent>
    </Card>
  );
}
