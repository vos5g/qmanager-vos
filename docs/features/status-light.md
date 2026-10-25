# Status Light (VOS build)

System Settings → Status Light turns the stick's LED off, always or between two times. Backend `system/led.sh` (`pkg/api/zz_vosclean_led.go`), hook `use-status-light.ts`, page `components/system-settings/status-light/`.

## How the LED is driven

Tri Cascade's `ceiled` service (systemd) sets the LED from the registration state, and only when that state changes:

* green: PMIC LED driver — `spmi0-08 0xC240` (enable) and `spmi0-09 0xBD46` (output), written through `/sys/kernel/debug/regmap/<dev>/{address,data}`;
* red: GPIO 15.

The same writes are in `/oem/script/led_off.sh` and `green_led_keep_on.sh`.

## Off / on

* **Off:** save the current register values and GPIO to `/tmp/qmanager_led_saved.json` (once), `systemctl stop ceiled`, write `0x0` to both registers and `0` to GPIO 15.
* **On:** write the saved values back (defaults `0x1` / `0x80` if none), then `systemctl start ceiled`. Starting ceiled does not redraw the LED by itself, so the restore must come first.
* Modes: `on`, `off`, `schedule` (`off_from`/`off_to` HH:MM in `timezone`, an IANA zone sent by the browser; the stick's clock is UTC; tzdata is embedded). A 30 s loop follows the schedule.
* QManager restores the light on shutdown. After a reboot ceiled runs normally until QManager applies the setting again.
* If QManager restarts while the light is off, the saved file plus an inactive ceiled tell it so.
