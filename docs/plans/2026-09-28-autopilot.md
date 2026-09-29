# Autopilot: self-healing OS upgrades

Booty rolls a new OS release across the fleet, checks that each node comes
back healthy, retries, downgrades to the last good release when it does not,
and for Bluefin Server reports the bad release upstream and moves on to the
next one.

## Decisions (user, 2026-09-28)

| Topic | Decision |
|---|---|
| Actuator | Booty is cluster-aware. **kured** if it exists in the cluster (the default); else the Kubernetes API (drain + reboot); else SSH when Booty is not in a cluster. |
| Healthy | Node `Ready` **and** the workloads that belong on the node scheduled and healthy, within **15 min**. |
| Reports | **No PII**: no hostname, MAC or IP. Machine and cluster information only. |
| Canary | `canary: true` host field; default the first non-control-plane host. |
| Bad release | After rollback, **retry R after 1 h**; a second failure downgrades for good and flags it. |
| Scope | **OS-agnostic**: health gate, retry, auto-downgrade and flagging for Flatcar, CoreOS and Bluefin. **Bluefin only**: the full autopilot — canary-serial rollout, filing the issue in `projectbluefin/server`, quarantining the release and trying the next one that is not in timeout. |

## Facts the design rests on

- A diskless Bluefin host re-images from Booty on every boot: there is no on-node upgrade and no boot counting (BIOS fleet). **Booty choosing what to serve a MAC is the whole upgrade *and* rollback mechanism.** Flatcar and CoreOS PXE hosts behave the same way (RAM root, re-image per boot).
- Today `/update-check` answers `rebootRequired:false` for Bluefin and the diskless node Ignition carries no `booty-update.timer`, `booty-booted` or health unit; `/bluefin/<mac>/` serves `current` (or `previous` by file name); there is no per-host version. Flatcar upgrades are already kured-driven fleet-wide through `/update-check`; CoreOS likewise by version compare.
- Booty caches `current` + `previous` for Bluefin only; Flatcar and CoreOS keep one version.
- Booty already talks to the Kubernetes API in-cluster (token minting; SA + Roles on `secrets`/`configmaps`) with plain `net/http`. kured is deployed in the homelab (`kured/kured.yaml`), one node at a time, sentinel `/var/run/reboot-required`.
- The homelab workers have no BMC: a **hung** node (not looping, not up) cannot be recovered automatically.

## Out of scope

Installed-mode Bluefin (systemd-sysupdate/boot-counting own that); firmware/BIOS updates; automatic conversion of Flatcar hosts to Bluefin; kured configuration (windows, lock) — Booty only sets sentinels; power control (no BMC); posting issues for Flatcar/CoreOS (draft only); k0s-specific health.

## Definitions

- **Release**: one cached version of an OS (`data/<os>/<version>/`).
- **`lastGood`** (per OS): the newest release every host that booted it passed the health gate on. Kept on disk until the whole fleet is healthy on a newer one (replaces the "one before the newest download" meaning of `previous`).
- **`targetVersion`** (per host): what Booty serves this MAC; empty = the fleet target for its OS.
- **Fleet target** (per OS): `current` unless held; the controller holds it at `lastGood` after a failure.
- **Health gate** (per host, from the reboot trigger `t0`, window **15 min**):
  - L0 *served*: kernel/UKI, initrd/DDI, Ignition fetched. A second kernel/UKI fetch before L1 = **boot loop**.
  - L1 *OS*: `POST /booted` with `running=<targetVersion>`; `POST /health` from `booty-health.service` after `multi-user.target`: `failedUnits` (names), `journalErrors` (redacted excerpt), `dmi` (vendor, product, BIOS version), `firmware` (bios/uefi), `kernel`. Gate: `failedUnits` empty.
  - L2 *cluster* (Kubernetes API): Node `Ready`, `nodeInfo.osImage`/`kubeletVersion` consistent with the target, no `Ready=False` transition in the last 5 min of the window; every DaemonSet that selects the node has its pod `Ready` on it; no pod on the node in `CrashLoopBackOff`/`Error`/`ImagePullBackOff`/`Pending>5m` that was not already in that state before `t0` (baseline snapshot).
  - Outcome: `healthy`, or `failed` with a class: `boot-loop`, `no-ignition`, `failed-units`, `node-not-ready`, `workloads-unhealthy`, `hung` (nothing fetched, node gone), `timeout`.
- **Attempt**: one reboot into a target. **Episode**: the attempts for one (host, release) pair.

## State machine (per OS release R, all OSes)

```
R cached ─► fleet target = R (Bluefin: canary first, then one host at a time;
            Flatcar/CoreOS: kured order as today)
host reboots into R ─► health gate
   healthy ─► lastGood candidate; when every host that runs this OS is healthy on R: lastGood = R
   failed  ─► attempt 2: reboot again (same target)
      healthy ─► continue
      failed  ─► targetVersion = lastGood, reboot, gate
                  healthy on lastGood ─► the release is bad, not the node:
                        fleet target held at lastGood; R enters TIMEOUT (1 h); draft report
                  failed on lastGood ─► the node is sick: NEEDS-HANDS, alert, leave on lastGood
R in TIMEOUT for 1 h ─► retry once on the canary
   healthy ─► resume rollout
   failed  ─► R QUARANTINED: stays on lastGood, flag (UI, /info, alert)
              Bluefin: post the issue (if enabled), pick the newest cached release that is
              neither quarantined nor in timeout and start again
```
`hung` counts as a failed attempt but no further reboot can be issued without an actuator that reaches the node; after the window it is NEEDS-HANDS.

Bluefin canary order: hosts with `canary: true` (sorted), else the first `role != control-plane` Bluefin host by MAC. Flatcar/CoreOS keep today's fleet-wide kured rollout, but the **first** failed host holds the fleet target, so a bad release stops after one node.

## Actuators (chosen at each use, in this order)

1. **kured** present (a DaemonSet named `kured` or labelled `app=kured` in any namespace): `/update-check` for the host answers `rebootRequired:true` while `targetVersion != running`; the node's `booty-update.timer` touches the sentinel; kured drains, reboots, uncordons. Booty never touches kured's lock.
2. **Kubernetes API** (in-cluster or `--kubeconfig`): cordon (`PATCH nodes/<n>` `spec.unschedulable`), evict pods (`pods/eviction`, honouring PDBs, skipping DaemonSet pods), then a **reboot Pod** on the node: Booty's own image, `hostPID: true`, privileged, `command: [/booty, node-reboot]` which sends `SIGRTMIN+5` to PID 1 (systemd: `reboot.target`) after `sync`. Uncordon after L2 passes. RBAC: `nodes` get/list/watch/patch, `pods` list/watch (all namespaces) + create/delete in Booty's namespace, `pods/eviction` create, `daemonsets` list.
3. **SSH** (`--rebootSSHKey`, users `root` for Bluefin, `core` + `sudo` for Flatcar/CoreOS): `systemctl reboot`; drain via the API first when a kubeconfig is available, else none.

The node name for API calls is the host's `hostname` (both OS paths set it from Booty); matching is confirmed via `nodeInfo.systemUUID` ↔ the `/health` DMI UUID hash where available, never by MAC in the report.

## Reporting (no PII)

A **report** is built for every quarantined release (all OSes) and written to `data/autopilot/reports/<os>-<version>.md` + `.json`, shown in the UI and linked from `/info`. Content: OS + release, `lastGood`, boot path (`bios-diskless`/`uefi-http`/`uefi-pxe`), hardware (DMI vendor, product, BIOS version), kernel, kubelet/Kubernetes version, CNI name+version, failure class per attempt, failed unit names, journal error excerpt **redacted** (hostname field dropped; IPv4/IPv6/MAC regexes → `<ip>`/`<mac>`; anything under `/bluefin/<mac>/` in URLs → `/bluefin/<host>/`), relative boot timeline (`t+0s UKI fetched`, `t+38s Ignition`, `t+…`), attempt count, and the rollback result ("lastGood X healthy on the same hardware"). Hostnames, MACs and IPs never enter the report; the test suite greps for them.

**Posting** (Bluefin only): `--autopilotIssues=<owner>/<repo>` (off by default) posts the report as an issue with `--githubToken` (needs `issues:write`), deduped by a hidden marker `<!-- booty-autopilot: bluefin <version> <dmi-hash> -->`; a second machine failing on the same release adds a comment. Recommended first use: your fork, then `projectbluefin/server`. Flatcar/CoreOS: draft only.

## Configuration

| Flag | Default | Meaning |
|---|---|---|
| `--autopilot` | `off` | `off`, `guard` (health gate + retry + downgrade + hold + draft, all OSes; rollout order unchanged), `full` (guard + Bluefin canary-serial rollout, timeout/retry, quarantine, skip-to-next) |
| `--autopilotHealthWindow` | `15m` | health gate window |
| `--autopilotRetryAfter` | `1h` | TIMEOUT before the single retry |
| `--autopilotIssues` | `""` | `owner/repo` to post Bluefin reports to |
| `--rebootSSHKey` | `""` | private key for the SSH actuator |
| `--autopilotNamespace` | Booty's own | namespace for reboot Pods |

Host fields: `canary` (bool), `targetVersion`, `autopilot` state (`idle`/`rolling`/`gating`/`retrying`/`rolled-back`/`needs-hands`, with attempt count and last class) — read-only in the UI except `canary`.

APIs: `GET /autopilot` (per-OS fleet target, `lastGood`, timeouts, quarantines, per-host state, actuator in use, reports), `POST /autopilot/{os}/release/{version}/clear` (un-quarantine), `POST /health` (node-side). `/update-check` gains the `targetVersion` rule for every OS. `/info.autopilot` summary.

## Slices / PRs

1. **P1 — signals and versions (all OSes)**: `booty-health.service` in the builtin fragment (new toggle `health`, default on; Flatcar/CoreOS goldens refreshed deliberately) and in the Bluefin node Ignition together with `booty-booted` and `booty-update.timer`; `POST /health`; `lastGood` + release retention for Flatcar and CoreOS (`data/flatcar/<v>/`, `data/coreos/<v>/` kept until fleet-healthy, `current`/`previous` links like Bluefin); per-host `targetVersion` honoured by every render (`/booty.ipxe`, `/bluefin/<mac>/`, `/ignition*`); `/update-check` answers `rebootRequired` from `targetVersion != running` for all OSes (Bluefin included). Homelab roll: byte-identical except the new health unit; `aren` re-PXE.
2. **P2 — cluster awareness and actuators**: read-only cluster client (nodes, pods, daemonsets; plain `net/http` like the minter), kured detection, actuator chain (sentinel / API drain + `booty node-reboot` Pod / SSH), `examples/k8s.yaml` + homelab `rbac.yaml` ClusterRole. Dry-run mode logs what it *would* do.
3. **P3 — controller**: health gate, episodes, retry, rollback, fleet hold, NEEDS-HANDS (all OSes, `--autopilot=guard`); Bluefin canary-serial rollout, TIMEOUT/retry, quarantine, skip-to-next (`full`); `GET /autopilot`, `/info.autopilot`, UI page (fleet targets, per-host state, timeline, reports) + `canary` in the host form.
4. **P4 — reports**: redacting report builder + tests (no hostname/MAC/IP), drafts on disk + UI, `--autopilotIssues` posting with dedupe/comments; verified against a scratch repo.
5. **P5 — verification**: QEMU kubeadm cluster (H2 harness) + kured + Bluefin workers; a deliberately **bad release** published to a local ORAS registry (wrong `usrhash=` in the netboot UKI's cmdline → dm-verity refuses `/usr`, boot loop) with `--autopilotRetryAfter=3m`: canary fails ×2 → rolls back to lastGood → timeout → retry fails → quarantined, report drafted (and posted to the scratch repo), fleet stays on lastGood; then a good release appears → rollout resumes. Also: `guard` on a Flatcar bad release (truncated initrd) → first node downgraded, fleet held. Then homelab: `aren` as Bluefin `canary: true` under `--autopilot=full`, left in place so the next upstream release exercises it for real.

Each PR: CI green before merge, image built/pushed, homelab rolled, fleet checked.

## Acceptance (agent-executable unless marked)

- Unit/golden: Flatcar/CoreOS renders unchanged apart from `booty-health.service` (fixture diff shows only that unit); Bluefin node Ignition carries booted/health/update units; `/update-check` table (os × targetVersion × running); retention keeps exactly `current`, `previous` and `lastGood` dirs; render for a host with `targetVersion=previous` points every URL at that release.
- Controller tests with a fake API server: each failure class from the definitions; the full episode sequence (fail, fail, rollback healthy → TIMEOUT → retry fail → QUARANTINED); `hung` → NEEDS-HANDS; fleet hold after first failure; Bluefin skip-to-next picks the newest non-quarantined; `clear` un-quarantines.
- Report tests: a report built from fixture data containing hostnames/MACs/IPs contains none of them (`grep -E '([0-9a-f]{2}:){5}|[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+|aren|ehrlitan'` = 0); dedupe marker present; posting test against an httptest GitHub API (create vs comment).
- Actuators: kured detection true/false against a fake API; API path issues cordon → evictions → reboot Pod with `hostPID` and `SIGRTMIN+5`; `booty node-reboot` unit-tested with a fake PID target (signal captured).
- QEMU (Sisyphus, manual, evidence pasted here): the bad-release episode above with timestamps; `GET /autopilot` JSON at each state; the drafted report; `kubectl get nodes` throughout (never more than one node down).
- Homelab: `aren` runs Bluefin as canary under `full`; `/autopilot` shows `idle` with `lastGood = current`.

## Risks

- No BMC: `hung` needs hands; the plan says so and alerts rather than pretending.
- A homelab-local failure (control plane down, registry unreachable) would look like a bad release: the gate's rollback-to-`lastGood` check on the *same* hardware is the discriminator; if `lastGood` also fails the release is not blamed and no issue is posted.
- kured windows may delay attempts well beyond 15 min: the gate clock starts at the observed reboot (L0 first fetch), not at sentinel time.
- Retaining three releases per OS costs disk (~2.5 GB Bluefin, ~1.2 GB Flatcar, ~3 GB CoreOS worst case).
- `--autopilot=full` on a fleet that is still Flatcar does nothing visible until a Bluefin host exists; `guard` is the mode that protects the current homelab.
