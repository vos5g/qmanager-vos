# vos-v1.1.0: Watchdog, alerts and a dashboard that explains slow connections

QManager for the Tri Cascade VOS 5G / SG500M2-X now looks after the connection by itself: a watchdog that recovers dropped connections, alerts and SMS forwarding that actually send, and buttons to reconnect or restart the stick without unplugging it. The dashboard also explains *why* a connection is slow, with thermal throttling, transmit-power reduction, uplink details and whether your cell offers 5G. Mostly new features, plus a few fixes.

## ✨ New Features

- **Reconnect and Restart buttons** in the top bar. Reconnect turns the radio off and on (about 15 s offline, usually a new IP); Restart answers the browser first, then restarts the stick (about 80 s).
- **Connection watchdog that really recovers** (Monitoring → Watchdog). After a few failed checks it restarts the data connection (~6 s), then the radio (~15 s), then the stick (~80 s). It restarts the stick at most once per outage and at most 3 times an hour, waits 3 minutes after boot, and its checks bypass WireGuard, so a VPN outage can't trigger it. Every step can be switched off.
- **Alerts by SMS and email**: connection lost (SMS), connection restored and stick restarted (SMS or email), with a log and a restart history. Email is sent directly over SMTP (Gmail, Outlook, iCloud and others, with an app password); no extra mailer needed.
- **SMS forwarding**: new texts are resent to another number as "From &lt;sender&gt;: &lt;message&gt;".
- **Data usage**: this billing month's total from the modem's own counters (hardware-offloaded traffic included), daily bars, your billing day and an optional allowance with warnings.
- **Why is it slow?** The dashboard now shows thermal throttling, transmit-power reduction (the stick turns its power down at start-up and briefly when it's moved), uplink and downlink modulation, transmit power, channel quality (CQI), the real MIMO layers in use, and whether the cell you're on offers 5G.
- **APN username, password and authentication** (PAP / CHAP). Needs the current `qcmd` (see below).
- Device Information shows the modem module (RXM-G1 BR23) and whether mmWave antenna modules are fitted.

## ✅ Improvements

- The header no longer says **OFFLINE** when everything works: it used to compare the stick's clock (which starts at 1970 until it's set) with your computer's.
- Connection Scenarios no longer shows Balanced, Gaming and Streaming twice.
- Reconnect used to send a command the modem ignores; it now really reconnects.
- The firmware version no longer shows a stray "Revision:" prefix, and the dashboard shows this release's version instead of upstream's.
- CI moved to the Node 24 versions of its GitHub Actions.

## 📥 Installation

Fresh install, or upgrade an existing one, from a clone of [vos5g-sdx55](https://github.com/vos5g/vos5g-sdx55) (pull it first: it has the clock fix and the `qcmd` APN changes):

```bash
./qcmd/build.sh
./install.sh --pubkey ~/.ssh/id_ed25519.pub --qmanager qmanager-core-armv7 --qcmd dist/qcmd-armv7
```

Check the download against `SHA256SUMS.txt`. QManager is MIT with Commons Clause (not for sale).

## 💙 Thank You

To evairo176 for the QManager UI this is built on, to the eko.one.pl forum thread for the VOS research, and to everyone testing on their own sticks.
