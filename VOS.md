# QManager for the Tri Cascade VOS 5G / SG500M2-X

This repository is a fork of [evairo176/qmanager](https://github.com/evairo176/qmanager) (QManager-GO, keeping its UI), adapted to the
**Tri Cascade VOS 5G USB dongle / TRITOM SG500M2-X M.2 module**, which is a Compal RXM-G1 on a Qualcomm SDX55.
The full upstream history is kept; the changes for this modem are separate commits on top of it.

The device guide, the on-device scripts (vosclean), the AT translator (qcmd), the backup tools and the
installer live in **[thalatamsainath/vos5g-sdx55](https://github.com/thalatamsainath/vos5g-sdx55)**.

## What's different from upstream

| Area | Change |
|---|---|
| Modem access | Every AT command goes through `qcmd`, which answers in Quectel format using Compal's `cricli`/`atcli` and QMI. No raw serial, no mock data |
| Storage | State lives under `/data/vosclean/qm` (the VOS rootfs is read-only) |
| Safety | Watchdog radio resets and reboots only with `QM_WATCHDOG=1`; self-update disabled; non-GET requests logged |
| Fixes | Serving-cell and CA parsing, Cellular Information fields, cell scanner results, `+CGCONTRDP` DNS |
| Custom DNS | Applied for real (dnsmasq upstream DNAT via `vosclean dns`) |
| Tailscale | Real install (Official download or Tiny build), login, routes, exit nodes |

`git log 2cc6795e..` lists every change with an explanation.

## Building

```bash
npm ci && npx next build                      # web UI
rm -rf backend/web/out && mkdir -p backend/web/out && cp -r out/. backend/web/out/
cd backend && CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
  go build -trimpath -ldflags="-s -w" -o ../dist/qmanager-core-armv7 ./cmd/server
```

Or push a tag `vos-vX.Y.Z`: the **VOS release** workflow builds `qmanager-core-armv7` and a Tailscale Tiny
binary and attaches them, with `SHA256SUMS.txt`, to a GitHub release.

## Following upstream

```bash
git remote add evairo176   https://github.com/evairo176/qmanager.git     # UI/base (branch dev-go)
git remote add latifangren https://github.com/latifangren/QManager-GO.git # features to cherry-pick
git fetch evairo176 && git merge evairo176/dev-go
```

## Credits and licence

QManager by [DrDolomite](https://github.com/dr-dolomite/QManager); QManager-GO by
[evairo176](https://github.com/evairo176/qmanager) and [latifangren](https://github.com/latifangren/QManager-GO).
Licensed **MIT with Commons Clause** (see [LICENSE](LICENSE)): free to use, modify and share, not to sell.
This fork is not affiliated with Tri Cascade, Compal, Quectel or Qualcomm.
