"use client";

import { useTranslation } from "react-i18next";
import { AlertTriangleIcon, Globe2 } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { useDnsProxy } from "@/hooks/use-dns-proxy";
import DNSServersCard from "./dns-servers-card";
import AdBlockingCard from "./ad-blocking-card";

const CustomDNSComponent = () => {
  const { t } = useTranslation("local-network");
  const { data } = useDnsProxy();
  return (
    <div className="@container/main mx-auto flex flex-col gap-6">
      <PageHeader icon={Globe2} title={t("dns.page_title")} description={t("dns.page_description")} />
      {data && data.vosclean_ready === false && (
        <Alert>
          <AlertTriangleIcon className="size-4 text-warning" />
          <AlertTitle>{t("dns_proxy.vosclean_old_title")}</AlertTitle>
          <AlertDescription>{t("dns_proxy.vosclean_old_body")}</AlertDescription>
        </Alert>
      )}
      <div className="grid grid-cols-1 @3xl/main:grid-cols-2 grid-flow-row items-start gap-4">
        <DNSServersCard />
        <AdBlockingCard />
      </div>
    </div>
  );
};

export default CustomDNSComponent;
