"use client";

import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Terminal } from "@xterm/xterm";
import type { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import {
  CheckCircle2Icon,
  Loader2Icon,
  MinusCircleIcon,
  RotateCcwIcon,
  XCircleIcon,
} from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { useWebConsole, type ConsoleFailure } from "@/hooks/use-web-console";

// Fixed dark palette: the terminal is the machine's own voice (DESIGN.md's
// Machine-Voice Rule), so it keeps one look in both themes.
const THEME = {
  background: "#0c0e14",
  foreground: "#e4e4e7",
  cursor: "#a5b4fc",
  selectionBackground: "#6366f155",
};

function failureKey(f: ConsoleFailure | null): string {
  if (!f) return "web_console.failure.dropped";
  if (f.kind === "refused") {
    if (f.code === 4003) return "web_console.failure.off";
    if (f.code === 4029) return "web_console.failure.busy";
    if (f.code === 4008) return "web_console.failure.timeout";
    return "web_console.failure.refused";
  }
  return `web_console.failure.${f.kind}`;
}

export function ConsoleTerminal() {
  const hostRef = useRef<HTMLDivElement | null>(null);
  const terminalRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const [ready, setReady] = useState(false);

  // Create xterm before the socket hook connects (the hook defers its first
  // connect to the next tick, and wires input once the socket opens).
  useEffect(() => {
    let disposed = false;
    let ro: ResizeObserver | null = null;
    void (async () => {
      const [{ Terminal }, { FitAddon }] = await Promise.all([
        import("@xterm/xterm"),
        import("@xterm/addon-fit"),
      ]);
      if (disposed || !hostRef.current) return;
      const term = new Terminal({
        cursorBlink: true,
        fontFamily: monoFamily(),
        fontSize: 13,
        scrollback: 3000,
        theme: THEME,
      });
      const fit = new FitAddon();
      term.loadAddon(fit);
      term.open(hostRef.current);
      try {
        fit.fit();
      } catch {
        // not visible yet
      }
      terminalRef.current = term;
      fitRef.current = fit;
      ro = new ResizeObserver(() => {
        try {
          fit.fit();
        } catch {
          // ignore
        }
      });
      ro.observe(hostRef.current);
      term.focus();
      setReady(true);
    })();
    return () => {
      disposed = true;
      ro?.disconnect();
      terminalRef.current?.dispose();
      terminalRef.current = null;
      fitRef.current = null;
    };
  }, []);

  return (
    <div className="grid gap-3">
      {ready ? (
        <ConnectionBar terminalRef={terminalRef} fitRef={fitRef} />
      ) : (
        <div className="h-6" />
      )}
      <div
        className="h-[60vh] min-h-80 overflow-hidden rounded-lg border p-2"
        style={{ background: THEME.background }}
        onClick={() => terminalRef.current?.focus()}
      >
        <div ref={hostRef} className="size-full" />
      </div>
    </div>
  );
}

function monoFamily(): string {
  const v =
    typeof document !== "undefined"
      ? getComputedStyle(document.body).getPropertyValue("--font-jetbrains-mono").trim()
      : "";
  return `${v ? v + ", " : ""}ui-monospace, Menlo, monospace`;
}

// Mounted only once xterm exists, so the socket handshake always carries the
// real terminal size and input is wired from the first keystroke.
function ConnectionBar({
  terminalRef,
  fitRef,
}: {
  terminalRef: React.RefObject<Terminal | null>;
  fitRef: React.RefObject<FitAddon | null>;
}) {
  const { t } = useTranslation("system-settings");
  const { connectionState, failure, reconnect } = useWebConsole({
    terminalRef,
    fitAddonRef: fitRef,
  });

  const connected = connectionState === "connected";
  const pending =
    connectionState === "connecting" || connectionState === "reconnecting";

  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        {connected ? (
          <Badge
            variant="outline"
            className="border-success/30 bg-success/15 text-success"
          >
            <CheckCircle2Icon className="size-3" />
            {t("web_console.state.connected")}
          </Badge>
        ) : pending ? (
          <Badge variant="outline" className="border-info/30 bg-info/15 text-info">
            <Loader2Icon className="size-3 animate-spin" />
            {t("web_console.state.connecting")}
          </Badge>
        ) : failure?.kind === "ended" ? (
          <Badge
            variant="outline"
            className="border-muted-foreground/30 bg-muted/50 text-muted-foreground"
          >
            <MinusCircleIcon className="size-3" />
            {t("web_console.state.ended")}
          </Badge>
        ) : (
          <Badge
            variant="outline"
            className="border-destructive/30 bg-destructive/15 text-destructive"
          >
            <XCircleIcon className="size-3" />
            {t("web_console.state.disconnected")}
          </Badge>
        )}
        {!connected && !pending && (
          <Button variant="outline" size="sm" onClick={reconnect}>
            <RotateCcwIcon className="size-4" />
            {t("web_console.button_reconnect")}
          </Button>
        )}
      </div>
      {!connected && !pending && (
        <p className="text-sm text-muted-foreground">{t(failureKey(failure))}</p>
      )}
    </div>
  );
}
