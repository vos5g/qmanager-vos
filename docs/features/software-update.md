# Software Update (VOS build)

System Settings → Software Update installs QManager releases from GitHub (`vos5g/qmanager-vos`, tags `vos-v*`). Backend `system/update.sh` (`pkg/api/zz_vosclean_update.go`), hook `use-qm-update.ts`, page `components/system-settings/software-update/software-update.tsx`.

## Flow

1. `GET` lists releases from the GitHub API (cached 10 min; 60 unauthenticated requests an hour). Drafts and releases without `qmanager-core-armv7` are skipped; newest first by version.
2. `install {tag}` downloads `qmanager-core-armv7` to `qmanager-core.download`, checks the size, the SHA-256 from the release's `SHA256SUMS.txt`, and that it is a 32-bit ARM ELF. If `/data` is short of space, the old `.prev` is removed first.
3. The running binary becomes `qmanager-core.prev`, the download becomes `qmanager-core`, `update_state.json` records the version replaced.
4. QManager sends itself SIGTERM (graceful shutdown: DNS handed back to vosclean, LED restored, data usage saved). systemd (`Restart=always`, `RestartSec=10`, set by `vosclean qm`) starts the new binary.
5. `rollback` swaps `qmanager-core` and `.prev` the same way (so it can be undone).

## Rollback guard

Before restarting, QManager writes `/data/vosclean/qm/etc/update_pending` and starts a one-shot systemd timer (`systemd-run --on-active=180 --timer-property=AccuracySec=1s`, outside QManager's cgroup). A new build (`StartUpdater`) removes the marker after running 45 s. If the marker is still there when the timer fires, the timer moves `.prev` back, writes `update_rolled_back` (the UI shows it) and restarts QManager.

* **AccuracySec matters:** the systemd default lets a timer fire up to a minute late.
* Releases older than v1.2.0 don't know the marker, so installing or rolling back to one skips the guard (`supportsGuard`). They have no update page either: `vosclean qm-rollback` (vos5g-sdx55) swaps the binaries from SSH.
* Verified on the stick: a broken binary crash-looped, and the guard restored the good one. A downgrade to v1.1.0 from GitHub, then `vosclean qm-rollback`, came back to the newer build.

## Version

The version shown is stamped at build time (`-X qmanager-backend/pkg/daemon.qmanagerVersionFallback=v1.2.0` by `vos-release.yml`); local builds say `dev` and are shown as development builds (no "update available").

## Tailscale Tiny

`tsInstallTiny` uses a binary uploaded with `install.sh --tailscale-tiny` if there is one; otherwise it downloads `tailscaled-tiny-armv7` from the newest release that has it, with the same SHA-256 check (`latestAsset` + `downloadAsset`).
