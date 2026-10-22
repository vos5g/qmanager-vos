"use client";

import { useState, useEffect, useMemo, useCallback } from "react";
import { useTranslation, Trans } from "react-i18next";

import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

import {
  Field,
  FieldError,
  FieldGroup,
  FieldLabel,
  FieldSet,
} from "@/components/ui/field";
import { Switch } from "@/components/ui/switch";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { SaveButton, useSaveFlash } from "@/components/ui/save-button";
import { InfoIcon } from "lucide-react";
import { toast } from "sonner";
import { useTtlSettings } from "@/hooks/use-ttl-settings";
import { useSimProfiles } from "@/hooks/use-sim-profiles";

// =============================================================================
// TTLSettingsCard — TTL/HL Configuration with SIM Profile Override
// =============================================================================
// When a Custom SIM Profile is active and has TTL > 0 or HL > 0, the
// form is disabled and an Alert banner informs the user that TTL/HL is
// managed by the active profile.  Same pattern as BandLocking ↔ Scenarios.
// =============================================================================

const TTLSettingsCard = () => {
  const { t } = useTranslation("local-network");
  const { data, isLoading, isSaving, error, saveTtlHl } = useTtlSettings();
  const {
    activeProfileId,
    getProfile,
    isLoading: profilesLoading,
  } = useSimProfiles();

  // --- SIM Profile override check (async) ------------------------------------
  const [profileOverride, setProfileOverride] = useState<{
    profileId: string;
    name: string;
  } | null>(null);

  useEffect(() => {
    if (!activeProfileId) return;

    let cancelled = false;
    (async () => {
      const profile = await getProfile(activeProfileId);
      if (cancelled) return;

      if (profile && (profile.settings.ttl > 0 || profile.settings.hl > 0)) {
        setProfileOverride({ profileId: activeProfileId, name: profile.name });
      } else {
        setProfileOverride(null);
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [activeProfileId, getProfile]);

  // Derive — no sync setState needed
  const isProfileControlled =
    !!activeProfileId && profileOverride?.profileId === activeProfileId;
  const profileName = isProfileControlled ? profileOverride.name : null;

  // --- Render ----------------------------------------------------------------
  const pageLoading = isLoading || profilesLoading;

  if (pageLoading) {
    return (
      <Card className="@container/card">
        <CardHeader>
          <CardTitle>{t("ttl.card_title")}</CardTitle>
          <CardDescription>{t("ttl.card_description")}</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="grid gap-4">
            <Skeleton className="h-8 w-48" />
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
          </div>
        </CardContent>
      </Card>
    );
  }

  if (data && !data.supported) {
    return <TTLUnsupportedCard />;
  }

  // Key-based remount — form reinitializes when data changes
  const formKey = data
    ? `${data.isEnabled}-${data.ttl}-${data.hl}`
    : "empty";

  return (
    <TTLForm
      key={formKey}
      data={data}
      isSaving={isSaving}
      error={error}
      saveTtlHl={saveTtlHl}
      isProfileControlled={isProfileControlled}
      profileName={profileName}
    />
  );
};

// VOS 5G: the IPA hardware fast path forwards traffic without the firewall, so
// TTL has to be set on the router in front of the stick.
function TTLUnsupportedCard() {
  const { t } = useTranslation("local-network");
  return (
    <Card className="@container/card">
      <CardHeader>
        <CardTitle>{t("ttl.card_title")}</CardTitle>
        <CardDescription>{t("ttl.unsupported_description")}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        <Alert>
          <InfoIcon className="size-4" />
          <AlertDescription>{t("ttl.unsupported_why")}</AlertDescription>
        </Alert>
        <div className="grid gap-2">
          <p className="text-sm font-medium">{t("ttl.unsupported_router_title")}</p>
          <p className="text-sm text-muted-foreground">{t("ttl.unsupported_router_body")}</p>
          <pre className="overflow-x-auto rounded-md border bg-muted/40 p-3 font-mono text-xs leading-relaxed">
{`# OpenWrt / GL.iNet (fw4): /etc/nftables.d/10-ttl.nft
chain mangle_postrouting_ttl65 {
  type filter hook postrouting priority 300; policy accept;
  oifname "usb0" ip ttl set 65
  oifname "usb0" ip6 hoplimit set 65
}
# then: fw4 reload

# older OpenWrt (iptables):
iptables  -t mangle -A POSTROUTING -o usb0 -j TTL --ttl-set 65
ip6tables -t mangle -A POSTROUTING -o usb0 -j HL --hl-set 65`}
          </pre>
          <p className="text-xs text-muted-foreground">{t("ttl.unsupported_router_note")}</p>
        </div>
      </CardContent>
    </Card>
  );
}

function TTLForm({
  data,
  isSaving,
  error,
  saveTtlHl,
  isProfileControlled,
  profileName,
}: {
  data: ReturnType<typeof useTtlSettings>["data"];
  isSaving: boolean;
  error: string | null;
  saveTtlHl: ReturnType<typeof useTtlSettings>["saveTtlHl"];
  isProfileControlled: boolean;
  profileName: string | null;
}) {
  const { t } = useTranslation("local-network");
  const { saved, markSaved } = useSaveFlash();

  // Form state initialized from data — no sync effect needed
  const [isEnabled, setIsEnabled] = useState(data?.isEnabled ?? false);
  const [ttlValue, setTtlValue] = useState(
    data && data.ttl > 0 ? String(data.ttl) : "",
  );
  const [hlValue, setHlValue] = useState(
    data && data.hl > 0 ? String(data.hl) : "",
  );

  const isDirty = useMemo(() => {
    if (!data) return false;
    const currentTtl = data.ttl > 0 ? String(data.ttl) : "";
    const currentHl = data.hl > 0 ? String(data.hl) : "";
    return (
      ttlValue !== currentTtl ||
      hlValue !== currentHl ||
      isEnabled !== data.isEnabled
    );
  }, [data, ttlValue, hlValue, isEnabled]);

  const handleToggle = useCallback((checked: boolean) => {
    setIsEnabled(checked);
    if (!checked) {
      setTtlValue("");
      setHlValue("");
    }
  }, []);

  const handleSave = useCallback(async () => {
    const ttl = isEnabled ? parseInt(ttlValue || "0", 10) : 0;
    const hl = isEnabled ? parseInt(hlValue || "0", 10) : 0;

    if (isEnabled && ttl === 0 && hl === 0) return;

    const success = await saveTtlHl(ttl, hl);
    if (success) {
      markSaved();
      toast.success(
        ttl > 0 || hl > 0
          ? t("ttl.toast_success_enabled", { ttl, hl })
          : t("ttl.toast_success_disabled"),
      );
    } else {
      toast.error(error || t("ttl.toast_error_apply"));
    }
  }, [isEnabled, ttlValue, hlValue, saveTtlHl, error, markSaved, t]);

  return (
    <Card className="@container/card">
      <CardHeader>
        <CardTitle>{t("ttl.card_title")}</CardTitle>
        <CardDescription>{t("ttl.card_description")}</CardDescription>
      </CardHeader>
      <CardContent>
        {isProfileControlled && (
          <Alert className="mb-4">
            <InfoIcon className="size-4" />
            <AlertDescription>
              <p>
                <Trans
                  i18nKey="ttl.alert_profile_override"
                  ns="local-network"
                  values={{ profileName }}
                  components={{ strong: <span className="font-semibold" /> }}
                />
              </p>
            </AlertDescription>
          </Alert>
        )}

        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            handleSave();
          }}
        >
          <FieldSet disabled={isProfileControlled}>
            <FieldGroup>
              <div className="grid gap-2">
                <Field orientation="horizontal" className="w-fit">
                  <FieldLabel htmlFor="ttl-setting">
                    {t("ttl.label_enable")}
                  </FieldLabel>
                  <Switch
                    id="ttl-setting"
                    checked={isEnabled}
                    onCheckedChange={handleToggle}
                    disabled={isProfileControlled}
                  />
                </Field>
              </div>

              <Field>
                <FieldLabel htmlFor="ttl-value">{t("ttl.label_ttl")}</FieldLabel>
                <Input
                  id="ttl-value"
                  type="number"
                  min="1"
                  max="255"
                  placeholder={t("ttl.placeholder_ttl")}
                  className="w-full"
                  value={ttlValue}
                  onChange={(e) => setTtlValue(e.target.value)}
                  disabled={!isEnabled || isProfileControlled}
                />
              </Field>

              <Field>
                <FieldLabel htmlFor="hl-value">
                  {t("ttl.label_hl")}
                </FieldLabel>
                <Input
                  id="hl-value"
                  type="number"
                  min="1"
                  max="255"
                  placeholder={t("ttl.placeholder_hl")}
                  className="w-full"
                  value={hlValue}
                  onChange={(e) => setHlValue(e.target.value)}
                  disabled={!isEnabled || isProfileControlled}
                />
              </Field>

              {isEnabled && !ttlValue && !hlValue && (
                <FieldError id="ttl-hl-error">
                  {t("ttl.error_at_least_one")}
                </FieldError>
              )}
            </FieldGroup>
          </FieldSet>
          <div>
            <SaveButton
              type="submit"
              isSaving={isSaving}
              saved={saved}
              label={t("actions.apply", { ns: "common" })}
              disabled={isProfileControlled || !isDirty}
            />
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

export default TTLSettingsCard;
