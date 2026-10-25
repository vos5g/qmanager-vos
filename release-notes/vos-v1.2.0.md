# vos-v1.2.0: Ad blocking, encrypted DNS and updates from GitHub

This release covers DNS and keeping QManager up to date. You can block ads and trackers for every device on the stick and encrypt DNS lookups with DNS over HTTPS or TLS. QManager can now update itself from GitHub releases, downgrade to an older release, or go back to the previous version. The status light can also be turned off at night. Mostly new features, with some polish.

## ✨ New Features

- **Software Update** (System Settings) installs releases straight from GitHub. Each download is checked against the release's `SHA256SUMS.txt` before it replaces anything.
  - **Downgrading:** you can also install any older release.
  - **Go back:** the version you replace stays on the stick, and Go back switches to it in one click.
  - **If a new version doesn't start,** the stick puts the old one back by itself within 3 minutes.
  - **Older releases** (v1.1.0 and earlier) have no update page. To come back from one, run `vosclean qm-rollback` over SSH.
- **Ad Blocking** applies to every device connected to the stick.
  - **Blocklists:** HaGeZi (Light / Normal / Pro), OISD Small, StevenBlack or your own URLs, refreshed weekly.
  - **Pause** for 5 minutes, an hour or until you resume, for when a site breaks.
  - **History:** blocked lookups per day for the last two weeks, plus the most-blocked domains, kept across restarts.
  - **Lists:** a never-block list, an always-block list, and a check for any single domain.
- **DNS Servers card**: one choice of Carrier DNS, Cloudflare, Quad9, Google, AdGuard, Control D, Mullvad or your own servers.
  - **Encryption:** plain, HTTPS (DoH) or TLS (DoT).
  - **Fallback:** if the encrypted connection fails, the same provider is asked without encryption. The card shows the address and fallback servers in use, plus lookup times.
- **Status Light** (System Settings) turns the stick's LED off, always or only at night, in your own time zone.
- **Tailscale Tiny** now downloads from the latest release on GitHub; you no longer have to upload it with `install.sh`.

## ✅ Improvements

- If QManager stops, DNS falls back to your plain servers or the carrier's within about two minutes. A device is never left pointing at a DNS proxy that isn't running.
- Local builds report themselves as development builds instead of an upstream version number.
- The Custom DNS page is now **DNS & Ad Blocking**. Its IPv6 fields were removed, since the stick never applied them.
- Code checks (golangci-lint) are clean for the new DNS and LED code.

## 📥 Installation

**Already on v1.1.0 or older:** upgrade once from a clone of [vos5g-sdx55](https://github.com/vos5g/vos5g-sdx55). Pull it first; it has the DNS redirect for ad blocking and `vosclean qm-rollback`.

```bash
./install.sh --pubkey ~/.ssh/id_ed25519.pub --qmanager qmanager-core-armv7
```

From v1.2.0 on, later releases install from **System Settings → Software Update**.

Check the download against `SHA256SUMS.txt`. QManager is MIT with Commons Clause (not for sale).

## 💙 Thank You

To evairo176 for the QManager UI this is built on, to HaGeZi, OISD and StevenBlack for their blocklists, and to everyone testing on their own sticks.
