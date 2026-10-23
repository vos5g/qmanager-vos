"use client";

import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { Loader2, Power, RotateCcw } from "lucide-react";

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
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useModemReconnect } from "@/hooks/use-modem-reconnect";
import { rebootNow } from "@/lib/reboot";

/** Top-bar power menu: reconnect to the mobile network, or restart the stick. */
export function DevicePowerMenu() {
  const { t } = useTranslation("common");
  const { reconnectModem, isReconnecting, step } = useModemReconnect();
  const [dialog, setDialog] = useState<"reconnect" | "restart" | null>(null);
  const [restarting, setRestarting] = useState(false);

  const busy = isReconnecting || restarting;

  const reconnect = async (e: React.MouseEvent) => {
    e.preventDefault();
    const res = await reconnectModem();
    setDialog(null);
    if (res.success) toast.success(t("power.reconnect_done"));
    else toast.error(t("power.reconnect_failed"), { description: res.error });
  };

  const restart = (e: React.MouseEvent) => {
    e.preventDefault();
    setRestarting(true);
    rebootNow("manual");
  };

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            className="size-8 text-muted-foreground hover:text-foreground"
            aria-label={t("power.menu_label")}
            disabled={busy}
          >
            {busy ? <Loader2 className="size-4 animate-spin" /> : <Power className="size-4" />}
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="min-w-60">
          <DropdownMenuItem onSelect={() => setDialog("reconnect")}>
            <RotateCcw />
            <div className="grid">
              <span>{t("power.reconnect")}</span>
              <span className="text-xs text-muted-foreground">{t("power.reconnect_hint")}</span>
            </div>
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem variant="destructive" onSelect={() => setDialog("restart")}>
            <Power />
            <div className="grid">
              <span>{t("power.restart")}</span>
              <span className="text-xs text-muted-foreground">{t("power.restart_hint")}</span>
            </div>
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <AlertDialog open={dialog === "reconnect"} onOpenChange={(o) => !isReconnecting && setDialog(o ? "reconnect" : null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("power.reconnect_title")}</AlertDialogTitle>
            <AlertDialogDescription aria-live="polite">{t("power.reconnect_description")}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={isReconnecting}>{t("actions.cancel")}</AlertDialogCancel>
            <AlertDialogAction disabled={isReconnecting} onClick={reconnect}>
              {isReconnecting ? (
                <>
                  <Loader2 className="size-4 animate-spin" />
                  {step === "disconnecting" ? t("state.disconnecting") : t("state.reconnecting")}
                </>
              ) : (
                t("power.reconnect_confirm")
              )}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={dialog === "restart"} onOpenChange={(o) => !restarting && setDialog(o ? "restart" : null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("power.restart_title")}</AlertDialogTitle>
            <AlertDialogDescription aria-live="polite">
              {restarting ? t("power.restart_sent") : t("power.restart_description")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={restarting}>{t("actions.cancel")}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" disabled={restarting} onClick={restart}>
              {restarting ? <Loader2 className="size-4 animate-spin" /> : t("power.restart_confirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
