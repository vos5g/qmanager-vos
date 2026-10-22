"use client";

import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChevronRightIcon, Loader2Icon } from "lucide-react";

import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { cn } from "@/lib/utils";
import type { HealthCheckTest } from "@/types/system-health-check";
import type { FetchTestOutput, TestOutput } from "@/hooks/use-system-health-check";
import { HealthStatusBadge } from "./health-status-badge";

// One check: label + one-line finding, expandable to the raw command output.
export function CheckRow({
  test,
  fetchOutput,
}: {
  test: HealthCheckTest;
  fetchOutput: FetchTestOutput;
}) {
  const { t } = useTranslation("system-settings");
  const [open, setOpen] = useState(false);
  const [output, setOutput] = useState<TestOutput | null>(null);
  const [loading, setLoading] = useState(false);
  const [failed, setFailed] = useState(false);
  const done = test.status !== "pending" && test.status !== "running";

  const onOpenChange = async (next: boolean) => {
    setOpen(next);
    if (!next || output || loading || !done) return;
    setLoading(true);
    setFailed(false);
    try {
      setOutput(await fetchOutput(test.id));
    } catch {
      setFailed(true);
    } finally {
      setLoading(false);
    }
  };

  return (
    <Collapsible open={open} onOpenChange={onOpenChange}>
      <CollapsibleTrigger
        disabled={!done}
        className="group flex w-full items-start gap-3 rounded-md px-2 py-2.5 text-left transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-default disabled:hover:bg-transparent"
      >
        <ChevronRightIcon
          className={cn(
            "mt-0.5 size-4 shrink-0 text-muted-foreground transition-transform",
            open && "rotate-90",
            !done && "opacity-0",
          )}
          aria-hidden="true"
        />
        <div className="min-w-0 flex-1">
          <p className="text-sm font-medium">{test.label}</p>
          {test.detail && (
            <p className="mt-0.5 break-words text-sm text-muted-foreground">
              {test.detail}
            </p>
          )}
        </div>
        <HealthStatusBadge status={test.status} className="shrink-0" />
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="ml-9 mr-2 mb-2">
          {loading ? (
            <p className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2Icon className="size-4 animate-spin" />
              {t("health_check.output_loading")}
            </p>
          ) : failed ? (
            <p className="text-sm text-destructive">
              {t("health_check.output_failed")}
            </p>
          ) : output ? (
            <>
              {output.truncated && (
                <p className="mb-1 text-xs text-muted-foreground">
                  {t("health_check.output_truncated")}
                </p>
              )}
              <pre className="max-h-72 overflow-auto rounded-md border bg-muted/40 p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap break-all">
                {output.output.trim() || t("health_check.output_empty")}
              </pre>
            </>
          ) : null}
        </div>
      </CollapsibleContent>
    </Collapsible>
  );
}
