"use client";

import { PageHeader } from "@/components/page-header";
import { USBModeCard } from "@/components/system-settings/modem-diagnostics/usb-mode-card";
import { Usb } from "lucide-react";

export default function USBModePage() {
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-6">
      <PageHeader icon={Usb} title="USB mode" />
      <USBModeCard />
    </div>
  );
}
