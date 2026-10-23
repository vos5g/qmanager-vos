"use client";

import { useState } from "react";
import { motion } from "motion/react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { AlertTriangleIcon, ShieldIcon } from "lucide-react";

import { PageHeader } from "@/components/page-header";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { containerVariants, itemVariants } from "@/lib/motion";
import { useWireGuard } from "@/hooks/use-wireguard";
import { WireGuardStatusCard } from "./wireguard-status-card";
import { WireGuardConfigsCard } from "./wireguard-configs-card";
import { WireGuardConfigDialog } from "./wireguard-config-dialog";

export default function WireGuardComponent() {
  const { t } = useTranslation("monitoring");
  const wg = useWireGuard();
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editName, setEditName] = useState<string | undefined>(undefined);
  const kernelAvailable = !!(wg.state?.kernel_present || wg.state?.kernel_loaded);

  const openAdd = () => {
    setEditName(undefined);
    setDialogOpen(true);
  };
  const openEdit = (name: string) => {
    setEditName(name);
    setDialogOpen(true);
  };

  return (
    <div className="@container/main mx-auto flex flex-col gap-6">
      <PageHeader icon={ShieldIcon} title={t("wireguard.page_title")} description={t("wireguard.page_description")} />
      {wg.error && !wg.state && (
        <Alert variant="destructive">
          <AlertTriangleIcon className="size-4" />
          <AlertDescription>{wg.error}</AlertDescription>
        </Alert>
      )}
      <motion.div
        className="grid grid-cols-1 gap-4 @3xl/main:grid-cols-2"
        variants={containerVariants}
        initial="hidden"
        animate="visible"
      >
        <motion.div variants={itemVariants}>
          <WireGuardStatusCard
            state={wg.state}
            busy={wg.busy}
            onDisconnect={() =>
              void wg.disconnect().then((r) =>
                r.success
                  ? toast.success(t("wireguard.toast_disconnected"))
                  : toast.error(t("wireguard.toast_failed"), { description: r.detail || r.error }),
              )
            }
          />
        </motion.div>
        <motion.div variants={itemVariants}>
          <WireGuardConfigsCard
            state={wg.state}
            busy={wg.busy}
            onAdd={openAdd}
            onEdit={openEdit}
            connect={wg.connect}
            disconnect={wg.disconnect}
            deleteConfig={wg.deleteConfig}
          />
        </motion.div>
      </motion.div>
      <WireGuardConfigDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        editName={editName}
        kernelAvailable={kernelAvailable}
        getConfig={wg.getConfig}
        saveConfig={wg.saveConfig}
        importConfig={wg.importConfig}
      />
    </div>
  );
}
