"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { toast } from "sonner";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Alert, AlertDescription } from "@/components/ui/alert";
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

const endpoint = "/cgi-bin/quecmanager/bands/unlock.sh";
type State = {
  supported: boolean;
  unlocked: boolean;
  helper_installed: boolean;
  verification_error?: string;
  backup: { id: string; stage: string } | null;
};
const stages: Record<string, string> = {
  backed_up: "Backup ready",
  writing: "Update interrupted — recovery needed",
  failed: "Update failed — backup retained",
  pending_unlock_reboot: "Unlock saved — reboot required",
  pending_restore_reboot: "Restore saved — reboot required",
  verified: "Verified after reboot",
  restored: "Original policy restored",
};

export function BandUnlockCard() {
  const [state, setState] = useState<State | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [action, setAction] = useState<"unlock" | "restore" | null>(null);
  const refresh = useCallback(async () => {
    try {
      const res = await fetch(endpoint, { cache: "no-store" });
      const data = await res.json();
      if (!res.ok || !data.success)
        throw new Error(data.detail || "Unable to inspect band policy");
      setState(data);
      setError(data.verification_error || "");
    } catch (e) {
      setError(
        e instanceof Error ? e.message : "Unable to inspect band policy",
      );
    }
  }, []);
  useEffect(() => {
    const id = setTimeout(() => void refresh(), 0);
    return () => clearTimeout(id);
  }, [refresh]);
  async function apply(which: "unlock" | "restore") {
    setBusy(true);
    setError("");
    let failure = "";
    try {
      const res = await fetch(endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ action: which, confirm: true }),
      });
      const data = await res.json();
      if (!res.ok || !data.success)
        throw new Error(data.detail || "Policy update failed");
      toast.success("Policy saved. Reboot when ready to activate it.");
    } catch (e) {
      failure = e instanceof Error ? e.message : "Policy update failed";
    } finally {
      await refresh();
      if (failure) setError(failure);
      setBusy(false);
    }
  }
  const pending = state?.backup?.stage.startsWith("pending_");
  const canUnlock =
    state?.supported &&
    !state.unlocked &&
    state.helper_installed &&
    (!state.backup || state.backup.stage === "restored");
  return (
    <Card>
      <CardHeader>
        <CardTitle>All-band unlock</CardTitle>
        <CardDescription>
          Use the bands reported by this modem’s hardware instead of the final
          regional policy.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <p className="text-sm text-muted-foreground">
          This changes the OEM and live modem policy together. It does not add
          hardware capabilities, carrier-aggregation combinations or change your
          band locks. USB mode is selected separately in System Settings.
        </p>
        {state && (
          <Badge variant="outline">
            {state.backup
              ? stages[state.backup.stage] || state.backup.stage
              : state.unlocked
                ? "Hardware policy active"
                : state.supported
                  ? "Supported policy"
                  : "Unknown policy — inspect only"}
          </Badge>
        )}
        {state && !state.helper_installed && (
          <p className="text-sm text-muted-foreground">
            Install the ARM DIAG helper to enable unlocking.{" "}
            <Link
              className="underline"
              href="/system-settings/modem-diagnostics"
            >
              Open modem diagnostics
            </Link>
          </p>
        )}
        {state?.backup && (
          <p className="text-xs text-muted-foreground break-all">
            Private recovery backup: /data/vosclean/qm/band-backups/
            {state.backup.id}. Keep a copy on your computer before removing
            QManager.
          </p>
        )}
        {error && (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <div className="flex flex-wrap gap-2">
          <Button
            disabled={!canUnlock || busy}
            onClick={() => setAction("unlock")}
          >
            {busy ? "Updating policy…" : "Unlock all hardware bands"}
          </Button>
          {state?.backup && (
            <Button
              variant="outline"
              disabled={
                busy ||
                !state.helper_installed ||
                state.backup.stage === "restored"
              }
              onClick={() => setAction("restore")}
            >
              Restore original policy
            </Button>
          )}
          {state?.backup && (
            <Button variant="outline" asChild>
              <a href={`${endpoint}?action=backup`}>Download private backup</a>
            </Button>
          )}
          {pending && (
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => rebootNow("manual")}
            >
              Reboot to activate
            </Button>
          )}
          <Button
            variant="ghost"
            disabled={busy}
            onClick={() => void refresh()}
          >
            Refresh status
          </Button>
        </div>
        <AlertDialog
          open={action !== null}
          onOpenChange={(open) => {
            if (!open) setAction(null);
          }}
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>
                {action === "restore"
                  ? "Restore the original band policy?"
                  : "Unlock all hardware bands?"}
              </AlertDialogTitle>
              <AlertDialogDescription>
                {action === "restore"
                  ? "The backup must match this physical modem. Both policy copies will be restored and read back."
                  : "QManager will make a private OEM and PolicyMan backup, then update and read back both policy copies. Unknown firmware or mismatched policies stop the operation. This can take several minutes; keep the modem powered."}{" "}
                A reboot is required afterwards and will interrupt the
                connection.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction
                onClick={() => {
                  const which = action;
                  setAction(null);
                  if (which) void apply(which);
                }}
              >
                Continue
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </CardContent>
    </Card>
  );
}
