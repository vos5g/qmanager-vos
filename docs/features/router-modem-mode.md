# Router modem mode (VOS)

Lets a USB host such as stock OpenWrt use the stick as its **QMI/MBIM modem** in USB mode `9064`. In that mode the host picks configuration 1 (QMI) and runs `proto qmi` / `uqmi`.

## Why it is needed

The stick keeps its own data call on the SIM's APN, which is QCMAP's "backhaul" and is used for NAT/ECM tethering. The network allows only one call on that APN, so the host's `uqmi --start-network` fails with `Call failed`. luci-app-5gmodem #53 reports the same failure. The fix is to drop the stick's call, and to stop QCMAP auto-connect from bringing it back, for as long as the host is using the modem.

## Who does what

| Layer | Piece |
|---|---|
| Daemon (vos5g-sdx55) | `vosclean/bin/hostmodem.sh`, systemd unit `vos-hostmodem`, setting `HOST_MODEM=auto\|off` in `/data/vosclean/etc/vosclean.conf`, CLI `vosclean hostmodem [on\|off]` |
| API | `system/host_modem.sh` (`backend/pkg/api/zz_vosclean_hostmodem.go`). `GET` returns `{supported, enabled, active, since, usb_pid}`; `POST {"enabled": bool}` runs `vosclean hostmodem on\|off` |
| Alerts / events | `zz_vosclean_notify.go`: while `active`, the outage tracking is reset, so no "connection lost" or "restored" alerts are sent. Each hand-over adds a `host_modem` event to Recent Activity |
| UI | `HostModemCard` (`components/system-settings/modem-diagnostics/host-modem-card.tsx`) on the USB mode page, hook `use-host-modem.ts` |

## Detection (daemon, every 2 s)

* **host:** UDC `configured`, the gadget has a `gsi.rmnet` or `gsi.mbim` function, and neither `ecm0` nor `rndis0` is `up`. Three checks in a row (about 6 s) are needed. The host then gets the connection: `cricli set_auto_connect_state 0`, then `backhaul_disconnect 1/2`.
* **anything else:** the stick reclaims the connection with `set_auto_connect_state 1` and `backhaul_connect 1`.

## Invariants

* **QCMAP persists the auto-connect flag** in `mobileap_cfg.xml`. The marker `/data/vosclean/state/hostmodem-released` exists exactly while vosclean holds the flag off. Whenever no host is using the modem, the daemon switches it back on and retries until that works. QCMAP ignores the change for about the first minute after boot. Turning the feature off and `uninstall.sh` also restore it. Never leave the flag at 0 without the marker, or the stick boots with no internet.
* **`active` = state file `mode=host` AND the marker exists.** `/tmp/vosclean/hostmodem` alone may be stale.
* **Watchdog:** while in host mode the daemon touches `/tmp/qmanager_radio_hold` every 2 s. `pkg/daemon/zz_vosclean_recovery.go` skips its recovery steps while that file is fresh. Without this, the watchdog would "repair" the missing call, up to rebooting the stick.
* **The unit uses `Restart=on-failure`.** The daemon exits 0 when the feature is off, so `Restart=always` would make it loop. `vosclean watch`, which runs every minute, restarts it if `HOST_MODEM=auto` and it isn't running.
* **QManager can't be reached in host mode** (there's no USB network), so the card's status only matters once tethering is back. The card is mainly where the feature is switched on and off.
* **GL.iNet** `cellular_manage` resets an unknown QMI modem through the USB power GPIO. Use stock OpenWrt.
