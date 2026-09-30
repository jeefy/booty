# Autopilot: self-healing OS upgrades

Booty rolls a new OS release across the fleet, checks that each node comes
back healthy, retries, downgrades to the last good release when it does not,
and for Bluefin Server reports the bad release upstream and moves on to the
next one.

## Decisions (user, 2026-09-28)

| Topic | Decision |
|---|---|
| Actuator | Booty is cluster-aware. **kured** if it exists in the cluster (the default); else SSH when `--rebootSSHKey` is set; else the Kubernetes API (drain + reboot Pod) as the last resort. *(Order changed 2026-09-30: was kured → API → SSH.)* |
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
2. **SSH** (`--rebootSSHKey`, users `root` for Bluefin, `core` + `sudo` for Flatcar/CoreOS): `systemctl reboot`; drain via the API first when a kubeconfig is available, else none.
3. **Kubernetes API** (in-cluster or `--kubeconfig`; the last resort): cordon (`PATCH nodes/<n>` `spec.unschedulable`), evict pods (`pods/eviction`, honouring PDBs, skipping DaemonSet pods), then a **reboot Pod** on the node: Booty's own image, `hostPID: true`, privileged, `command: [/booty, node-reboot]` which sends `SIGRTMIN+5` to PID 1 (systemd: `reboot.target`) after `sync`. Uncordon after L2 passes. RBAC: `nodes` get/list/watch/patch, `pods` list/watch (all namespaces) + create/delete in Booty's namespace, `pods/eviction` create, `daemonsets` list.

**Changed 2026-09-30**: the order was kured → API → SSH; it is now kured → SSH → API. An operator who hands Booty an SSH key has chosen how nodes are rebooted; the privileged hostPID reboot Pod is the last resort when neither kured nor a key exists. SSH keeps draining through the API client when one is reachable.

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
4. **P4 — reports**: redacting report builder + tests (no hostname/MAC/IP), drafts on disk + UI, `--autopilotIssues` posting with dedupe/comments; verified against a scratch repo. *Done (PR feat/autopilot-p4): `pkg/autopilot/report` (builder, `Redact`, GitHub poster), `data/autopilot/reports/<os>-<version>.{md,json}`, `GET /autopilot/reports/…`, reports in the UI with the issue link; live-verified against `jeefy/booty-autopilot-scratch` (1 issue + 1 comment + 1 no-op).*
5. **P5 — verification**: QEMU kubeadm cluster (H2 harness) + kured + Bluefin workers; a deliberately **bad release** published to a local ORAS registry (wrong `usrhash=` in the netboot UKI's cmdline → dm-verity refuses `/usr`, boot loop) with `--autopilotRetryAfter=3m`: canary fails ×2 → rolls back to lastGood → timeout → retry fails → quarantined, report drafted (and posted to the scratch repo), fleet stays on lastGood; then a good release appears → rollout resumes. Also: `guard` on a Flatcar bad release (truncated initrd) → first node downgraded, fleet held. Then homelab: `aren` as Bluefin `canary: true` under `--autopilot=full`, left in place so the next upstream release exercises it for real. *QEMU part done (PR docs/autopilot-p5, evidence below): the full Bluefin episode ran end to end against kured with the classes the plan predicts; four controller/server bugs it surfaced are fixed in the same PR. Not done: the Flatcar `guard` run (no clean way to make Booty believe in a Flatcar release that does not exist upstream), the scratch-repo post (P4 covered it; this run kept reports on disk) and the homelab `aren` step.*

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

## Evidence (P5)

QEMU run by Sisyphus, 2026-09-29 05:50–07:02 UTC, bridged lab `br-booty` 10.77.0.0/24 (dnsmasq DHCP + TFTP, NAT). Booty built from `main` `b1cfb0e`, then rebuilt on this branch as fixes landed (which binary was running when is in the notes). Times below are UTC; the run's own artefacts (`GET /autopilot` every 20 s, `kubectl get nodes` every 20 s, `state.json`, the reports, serial logs) are in the lab's `p5-logs/`.

### Setup

- **Cluster**: kubeadm managed control plane from the H2 harness — `cp1` (Flatcar 4757.2.0, OVMF, 4 GiB, `--controlPlaneDisk=/dev/vdb --containerdDisk=/dev/vda --cni=cilium`), Ready with Cilium 7 min after power-on. Bluefin kubeadm workers only exist under `--controlPlane=external --profile=kubeadm-worker`, so Booty was then restarted in that mode with `--kubeconfig=<cp1's /etc/kubernetes/admin.conf>` (also the autopilot's cluster client) and `--autopilot=full --autopilotRetryAfter=3m --autopilotHealthWindow=6m --updateSchedule='*/2 * * * *'`. `cp1` never reboots (kured's DaemonSet has no control-plane toleration).
- **Workers**: two Bluefin Server hosts, **legacy BIOS diskless** (SeaBIOS → QEMU's iPXE ROM → `undionly.kpxe` → `/booty.ipxe` `:bios` branch → `.linux`/`.initrd` sections), 4 GiB each, a pre-formatted ext4 `ssd` disk as `--containerdDisk`. `w-bf1` registered with `canary: true`; both with a per-host Butane that drops `OnCalendar=*:0/2 RandomizedDelaySec=10` onto `booty-update.timer` (lab only, so kured saw the sentinel within 2 min instead of 10). kured 1.22.1 from the homelab manifest with `--period=1m --drain-timeout=10m --lock-ttl=30m`, workers only.
- **Releases** came from a local plain-HTTP ORAS registry (`podman run registry:2`, `--bluefinOCI=http://127.0.0.1:5055/bluefin-server`, `latest` unpinned), pushed with `oras push`:
  - `26.09.673` — the pristine upstream release: UKI, DDI, `SHA256SUMS`, `SHA256SUMS.gpg` and the original `kubeadm_26.09.673.raw.zst` (the other sysexts were not published; Booty logs *listed in SHA256SUMS but not published; skipping* and the node config carries only kubeadm).
  - `26.09.674` — **the bad release**, built from 673: the DDI and kubeadm sysext byte-identical under `_26.09.674` names, the netboot UKI's `.cmdline` PE section patched in place (601 → 658 bytes within the section's 1 KiB raw size, `VirtualSize` updated) to `usrhash=aa3f2925…` (first hex digit flipped), `bootorigin:rootdisk:bluefin-server_26.09.674.raw`, `verify=checksum` instead of `verify=signature` (the regenerated `SHA256SUMS` cannot carry the upstream signature; `.gpg` is 673's, unused) and `systemd.verity_usr_options=restart-on-corruption panic=10`. Without that last argument the initrd drops to its emergency path instead of looping: it has no `rd.emergency=` and dm-verity's default is EIO. Verified on a throwaway VM before the run: `device-mapper: verity: 259:0: metadata block 1 is corrupted` → `reboot: Restarting system with command 'dm-verity device corrupted'`, one cycle every ≈50 s, no Ignition fetch before the restart. `objcopy --update-section` was not usable: it rewrites the PE layout and truncated the section.
  - `26.09.678` — the **real upstream release** published on 2026-09-29 04:14Z (UKI, DDI, `SHA256SUMS(.gpg)`, `kubeadm_26.09.678.raw.zst`, all verified against its signed `SHA256SUMS`), used as the "good next release" instead of a renamed copy of 673: the node reports `running` from the DDI's `/etc/os-release` `VERSION_ID`, so a renamed 673 would boot fine but report `26.09.673`, the gate would say *health report says the host runs 26.09.673, not 26.09.675* and `/update-check` would keep asking for reboots — the renaming trick only works for a release that is meant to fail before the OS is up.

### Timeline

Condensed from the 20-second poll (`act=kured` throughout once kured was deployed at 05:58Z; `nodes` is `kubectl get nodes`). A run of the first-boot gates on 673 between 05:55 and 06:11 is described under *Misbehaviour 1* and was discarded (state reset) before the episode below started; nothing else was reset or replayed.

| UTC | Event | 26.09.674 | canary `w-bf1` (…:51) | fleet target / lastGood | nodes Ready |
|---|---|---|---|---|---|
| 06:13:38 | `oras push …:26.09.674,latest` | — | idle, healthy on 673 | 673 / 673 | cp1 w-bf1 w-bf2 |
| 06:14:10 | Booty's 2-min sync caches 674, `current` = 674 | — | idle | **674** / 673 for ≤ 40 s (*Misbehaviour 2*) | 3/3 |
| 06:14:31 | controller: `episode started: canary-serial rollout of 26.09.674`, host pinned, `reboot delegated to kured`, `fleet target held at 26.09.673` | rolling | rolling, attempt 1, pinned 674 | 673 (held) / 673 | 3/3 |
| 06:15:30 | kured reboots **`w-bf2`** into 673 (the non-canary; *Misbehaviour 2*) | rolling | rolling | 673 / 673 | cp1 w-bf1 (w-bf2 NotReady 40 s) |
| 06:18:11–06:18:35 | kured drains and reboots the canary; `.linux` fetch of 674 = `t0`, `health gate started` | rolling | gating, attempt 1 | 673 / 673 | cp1 w-bf2 |
| 06:19:23 | second `.linux` fetch 48 s later → **`boot-loop`**; `attempt 2 … delegated to kured` (the loop supplies it) | rolling, failing | retrying → gating, attempt 2 | 673 / 673 | cp1 w-bf2 |
| 06:20:58 | again 50 s later → **`boot-loop`**; `rolling back to lastGood 26.09.673` | rolling, failing | rolled-back, attempt 3, pinned 673 | 673 / 673 | cp1 w-bf2 |
| 06:21–06:26 | the loop iteration that had already fetched 674's kernel asks for `SHA256SUMS`, gets **673's**, `systemd-pull` refuses the DDI, the initrd waits out its 300 s device timeout, Bluefin's emergency path reboots it (*Misbehaviour 3*) | | rolled-back, *waiting for the host to reboot* | | cp1 w-bf2 |
| 06:26:49 | kernel fetch of **673**, gate started (5 m 51 s after the request — 9 s before `hung` would have fired) | | gating, attempt 3 | | cp1 w-bf2 |
| 06:27:33 / 06:28:14 | node Ready / uncordoned | | gating | | 3/3 |
| 06:30:31 | `healthy on lastGood 26.09.673: the release is bad, not the node` → **TIMEOUT** for 3 m, `report drafted (3 attempt(s), last class boot-loop)`, pin cleared | **timeout** | rolled-back, done, healthy on 673 | 673 (held) / 673 | 3/3 |
| 06:34:01 | `TIMEOUT elapsed; single retry` on the canary, pinned 674, kured | timeout, retried | retrying, attempt 1 | 673 / 673 | 3/3 |
| 06:34:48 → 06:35:35 | drain, reboot, `.linux` 674 fetched twice 46 s apart → **`boot-loop`** → **QUARANTINED**: `the retry failed too; the fleet stays on lastGood`, `report final (4 attempt(s))`, `back to lastGood 26.09.673 after the failed retry` | **quarantined** | rolled-back, attempt 2, pinned 673 | 673 / 673 | cp1 w-bf2 |
| 06:35–06:41 | same 300 s `SHA256SUMS` stall as above (*Misbehaviour 3*), then kernel fetch of 673 at 06:41:24 (5 m 49 s) | | gating | | cp1 w-bf2 |
| 06:45:31 | `healthy on lastGood 26.09.673` again, pin cleared; **`w-bf2` never left 673** apart from the 06:15 reboot | quarantined | rolled-back, done | 673 (held) / 673 | 3/3 |
| 06:46:38 | Booty restarted on the fixed binary (state resumed from `state.json`: same episodes, same quarantine) | | | | 3/3 |
| 06:47:01 | `oras push …:26.09.678,latest` | | | | 3/3 |
| 06:48:14 | sync caches 678; **skip-to-next**: `episode started: canary-serial rollout of 26.09.678` (674 is quarantined, 678 is the newest non-bad release), canary pinned, kured; **`w-bf2` does not move** this time (fleet target is 673 from the instant 678 lands — the fix) | quarantined | rolling → gating (06:51:01) | 673 (held) / 673 | 3/3 → cp1 w-bf2 → 3/3 |
| 06:55:14 | `attempt 1 into 26.09.678 healthy` (Ready 06:51:59, +3 min stability, workloads healthy) → `episode started: canary-serial rollout of 26.09.678` for **`w-bf2`** | | idle, healthy on 678 | 673 / 673 | 3/3 |
| 06:56:56 → 07:00:44 | `w-bf2` drained, rebooted, gated, `healthy` | | | | cp1 w-bf1 → 3/3 |
| 07:00:44 | `every host is healthy on it: lastGood = 26.09.678`, `fleet target released; back to the current release`, both pins cleared | quarantined | idle | **678 / 678** | 3/3 |

At no point during the episodes were two nodes NotReady at once (`grep -c 'NotReady.*NotReady'` over the poll = 1, the initial join at 05:56 before kured existed). kured's lock serialised every reboot; the controller never asked for two.

`kubectl get nodes -o wide` at 07:02Z:

```
NAME    STATUS   ROLES           AGE   VERSION   INTERNAL-IP   OS-IMAGE                                      KERNEL-VERSION
cp1     Ready    control-plane   71m   v1.34.3   10.77.0.30    Flatcar Container Linux by Kinvolk 4757.2.0   6.12.109-flatcar
w-bf1   Ready    <none>          67m   v1.34.3   10.77.0.183   Bluefin Server 26.09.678                      7.2.2
w-bf2   Ready    <none>          66m   v1.34.3   10.77.0.184   Bluefin Server 26.09.678                      7.2.2
```

`data/bluefin/` afterwards: `26.09.673`, `26.09.674`, `26.09.678`, `current → 26.09.678`, `previous → 26.09.674`, `lastGood → 26.09.678` (retention keeps the quarantined release as `previous`; a fourth release would prune it).

### Classes that fired, against the plan

| Plan | Observed |
|---|---|
| canary fails ×2 with `boot-loop` | yes, both attempts: `kernel fetched 2 times before the OS came up`, 48 s and 50 s apart, no Ignition fetch in between; the loop supplied attempt 2 itself, the kured request was never needed for it |
| rollback to lastGood, healthy on the same hardware → TIMEOUT, report drafted | yes (`lastGood 26.09.673 healthy on the same hardware`), pin cleared, fleet held |
| retry once after `--autopilotRetryAfter` on the canary | yes, at exactly +3 m 30 s (one tick late), through kured |
| retry fails → QUARANTINED, report final, host back on lastGood | yes; `rollbackResult: retry failed with boot-loop` |
| skip-to-next picks the newest non-quarantined release, canary first, one host at a time, lastGood advances when all are healthy, pins cleared | yes, all of it, on a real upstream release |
| `hung` / NEEDS-HANDS | not part of the intended episode; it fired once during the discarded first-boot phase when kured rebooted the canary while Booty was down for a restart (below) — correct classification of what the controller could see: reboot requested, node NotReady, nothing fetched for a window |
| `no-ignition`, `failed-units`, `node-not-ready`, `workloads-unhealthy` | `node-not-ready` fired (wrongly, *Misbehaviour 1*) on the first-boot gates; the others did not come up in this scenario |

### `GET /autopilot` at each state

Trimmed to the Bluefin, hosts and reports parts (`healthWindow: 6m0s`, `retryAfter: 3m0s`, the Flatcar/CoreOS blocks and the 200-event ring are omitted; the full snapshots are in `p5-logs/snap-*.json`). Baseline and TIMEOUT/QUARANTINED were taken from the binary running at the time; the last one after the restart on the fixed binary.

**Baseline, 06:13Z, before the bad release was published**

```json
{
 "mode": "full",
 "actuator": "kured",
 "cluster": { "reachable": true, "kured": true, "nodes": 3, "apiServer": "https://10.77.0.30:6443" },
 "os": {
  "bluefin": {
   "fleetTarget": "26.09.673",
   "current": "26.09.673",
   "lastGood": "26.09.673",
   "held": false,
   "releases": [
    {"version": "26.09.673", "state": "good"}
   ]
  }
 },
 "hosts": [],
 "reports": [],
 "held": [],
 "quarantined": 0,
 "needsHands": 0
}
```

**TIMEOUT, 06:30Z**

```json
{
 "mode": "full",
 "actuator": "kured",
 "cluster": { "reachable": true, "kured": true, "nodes": 3, "apiServer": "https://10.77.0.30:6443" },
 "os": {
  "bluefin": {
   "fleetTarget": "26.09.673",
   "current": "26.09.674",
   "lastGood": "26.09.673",
   "held": true,
   "releases": [
    {"version": "26.09.674", "state": "timeout", "attempts": 2, "class": "boot-loop", "failedOn": "52:54:00:aa:00:51", "report": "bluefin-26.09.674"},
    {"version": "26.09.673", "state": "good"}
   ]
  }
 },
 "hosts": [
  {
   "mac": "52:54:00:aa:00:51",
   "healthyOn": "26.09.673",
   "pinned": false,
   "episode": {
    "release": "26.09.674",
    "target": "26.09.673",
    "attempt": 3,
    "state": "rolled-back",
    "class": "boot-loop",
    "actuator": "kured",
    "rollingBack": true,
    "done": true,
    "note": "node Ready, workloads healthy",
    "attempts": [
     {"attempt": 1, "target": "26.09.674", "outcome": "failed", "class": "boot-loop", "timeline": ["t+0s kernel fetched", "t+48s kernel fetched again"]},
     {"attempt": 2, "target": "26.09.674", "outcome": "failed", "class": "boot-loop", "timeline": ["t+0s kernel fetched", "t+50s kernel fetched again"]},
     {"attempt": 3, "target": "26.09.673", "outcome": "healthy", "timeline": ["t+0s kernel fetched", "t+22s ignition fetched", "t+30s OS up (booted)", "t+31s health reported: 0 failed unit(s)"]}
    ]
   }
  }
 ],
 "reports": [
  {"key": "bluefin-26.09.674", "draft": true, "class": "boot-loop", "attempts": 3, "rollbackResult": "lastGood 26.09.673 healthy on the same hardware"}
 ],
 "held": ["bluefin"],
 "quarantined": 0,
 "needsHands": 0
}
```

**QUARANTINED, 06:36Z**

```json
{
 "mode": "full",
 "actuator": "kured",
 "cluster": { "reachable": true, "kured": true, "nodes": 3, "apiServer": "https://10.77.0.30:6443" },
 "os": {
  "bluefin": {
   "fleetTarget": "26.09.673",
   "current": "26.09.674",
   "lastGood": "26.09.673",
   "held": true,
   "releases": [
    {"version": "26.09.674", "state": "quarantined", "attempts": 3, "class": "boot-loop", "failedOn": "52:54:00:aa:00:51", "retried": true, "report": "bluefin-26.09.674"},
    {"version": "26.09.673", "state": "good"}
   ]
  }
 },
 "hosts": [
  {
   "mac": "52:54:00:aa:00:51",
   "healthyOn": "26.09.673",
   "pinned": true,
   "episode": {
    "release": "26.09.674",
    "target": "26.09.673",
    "attempt": 2,
    "state": "rolled-back",
    "class": "boot-loop",
    "actuator": "kured",
    "retry": true,
    "rollingBack": true,
    "note": "waiting for the host to reboot",
    "attempts": [
     {"attempt": 1, "target": "26.09.674", "outcome": "failed", "class": "boot-loop", "timeline": ["t+0s kernel fetched", "t+46s kernel fetched again"]}
    ]
   }
  }
 ],
 "reports": [
  {"key": "bluefin-26.09.674", "draft": false, "class": "boot-loop", "attempts": 4, "rollbackResult": "retry failed with boot-loop"}
 ],
 "held": ["bluefin"],
 "quarantined": 1,
 "needsHands": 0
}
```

**Rollout of 26.09.678 complete, 07:02Z**

```json
{
 "mode": "full",
 "actuator": "kured",
 "cluster": { "reachable": true, "kured": true, "nodes": 3, "apiServer": "https://10.77.0.30:6443" },
 "os": {
  "bluefin": {
   "fleetTarget": "26.09.678",
   "current": "26.09.678",
   "lastGood": "26.09.678",
   "held": false,
   "releases": [
    {
     "version": "26.09.678",
     "state": "good",
     "attempts": 2,
     "healthy": ["52:54:00:aa:00:51", "52:54:00:aa:00:52"]
    },
    {"version": "26.09.674", "state": "quarantined", "attempts": 3, "class": "boot-loop", "failedOn": "52:54:00:aa:00:51", "retried": true, "report": "bluefin-26.09.674"},
    {"version": "26.09.673", "state": "good"}
   ]
  }
 },
 "hosts": [
  {
   "mac": "52:54:00:aa:00:51",
   "healthyOn": "26.09.678",
   "pinned": false,
   "episode": {
    "release": "26.09.678",
    "target": "26.09.678",
    "attempt": 1,
    "state": "idle",
    "actuator": "kured",
    "done": true,
    "note": "node Ready, workloads healthy",
    "attempts": [
     {"attempt": 1, "target": "26.09.678", "outcome": "healthy", "timeline": ["t+0s kernel fetched", "t+25s ignition fetched", "t+33s OS up (booted)", "t+34s health reported: 0 failed unit(s)"]}
    ]
   }
  },
  {
   "mac": "52:54:00:aa:00:52",
   "healthyOn": "26.09.678",
   "pinned": false,
   "episode": {
    "release": "26.09.678",
    "target": "26.09.678",
    "attempt": 1,
    "state": "idle",
    "actuator": "kured",
    "done": true,
    "note": "node Ready, workloads healthy",
    "attempts": [
     {"attempt": 1, "target": "26.09.678", "outcome": "healthy", "timeline": ["t+0s kernel fetched", "t+26s ignition fetched", "t+34s OS up (booted)", "t+35s health reported: 0 failed unit(s)"]}
    ]
   }
  }
 ],
 "reports": [
  {"key": "bluefin-26.09.674", "draft": false, "class": "boot-loop", "attempts": 4, "rollbackResult": "retry failed with boot-loop"}
 ],
 "held": [],
 "quarantined": 1,
 "needsHands": 0
}
```

### The report

`data/autopilot/reports/bluefin-26.09.674.md`, verbatim as rendered at quarantine (06:35:35Z) by the binary running at the time (before fixes 4 and 5 below — see the notes under it). `grep -cE '([0-9a-f]{2}:){5}|[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+|w-bf1|w-bf2|cp1' bluefin-26.09.674.md` = **0**; `dmi-hash` is `unknown` because QEMU without `-uuid` has no `/sys/class/dmi/id/product_uuid` at all.

````markdown
# Bluefin Server 26.09.674: boot-loop (autopilot report)

<!-- booty-autopilot: bluefin 26.09.674 unknown -->

Booty's autopilot (`--autopilot=full`) rolled this release out, watched it fail the health gate 4 times and rolled the host back. Status: **quarantined**.

## Release

| | |
|---|---|
| OS | Bluefin Server |
| Release | `26.09.674` |
| lastGood | `26.09.673` |
| Attempts | 4 |
| Last failure class | `boot-loop` |
| Rollback | retry failed with boot-loop |

## Machine

| | |
|---|---|
| Boot path | `bios-diskless` |
| Hardware | QEMU Standard PC (Q35 + ICH9, 2009) |
| BIOS | 1.17.0-10.fc44 |
| Firmware | bios |
| Kernel | 7.2.2 |
| kubelet | v1.34.3 |
| Node osImage | Bluefin Server 26.09.673 |
| Container runtime | containerd://2.1.5 |
| Machine id | dmi-hash `unknown` (sha256 of the DMI product UUID, first 8 bytes) |

## Attempts

### Attempt 1 into `26.09.674`: failed (`boot-loop`)

kernel fetched 2 times before the OS came up

Timeline (relative to the kernel/UKI fetch):

```
t+0s kernel fetched
t+48s kernel fetched again
```

### Attempt 2 into `26.09.674`: failed (`boot-loop`)

kernel fetched 2 times before the OS came up

Timeline (relative to the kernel/UKI fetch):

```
t+0s kernel fetched
t+50s kernel fetched again
```

### Attempt 3 into `26.09.673`: healthy

node Ready, workloads healthy

Timeline (relative to the kernel/UKI fetch):

```
t+0s kernel fetched
t+22s ignition fetched
t+30s OS up (booted)
t+31s health reported: 0 failed unit(s)
```

### Attempt 1 into `26.09.674`: failed (`boot-loop`)

kernel fetched 2 times before the OS came up

Timeline (relative to the kernel/UKI fetch):

```
t+0s kernel fetched
t+46s kernel fetched again
```

## Journal errors (redacted excerpt)

Error-level lines from the failing boot's journal (`journalctl -p err -b --no-hostname`). Hostnames, addresses, MACs, UUIDs and keys are replaced by `<host>`, `<ip>`, `<mac>`, `<uuid>`, `<key>`.

```
2026-09-29T06:27:14+00:00 systemd[1]: Failed to preset all unit: Unit /run/systemd/generator.early/boot.mount is masked
2026-09-29T06:27:14+00:00 systemd[1]: Failed to preset all unit: Unit /run/systemd/generator.early/boot.automount is masked
2026-09-29T06:27:14+00:00 systemd[1]: Failed to preset all unit: Unit /run/systemd/generator.early/systemd-repart.service is masked
2026-09-29T06:27:14+00:00 systemd[1]: Failed to preset all unit: Unit /etc/systemd/system/systemd-loop@var-lib-machines-rootdisk.raw.service is masked
2026-09-29T06:27:14+00:00 systemd[1]: Failed to preset all unit: Unit /etc/systemd/system/systemd-import@var-lib-machines-rootdisk.raw.service is masked
2026-09-29T06:27:14+00:00 systemd[1]: Failed to preset all unit: Unit /etc/systemd/system/systemd-homed-firstboot.service is masked
2026-09-29T06:27:14+00:00 systemd[1]: Failed to preset all unit: Unit /run/systemd/generator/sshd-unix-local.socket is transient or generated
2026-09-29T06:27:14+00:00 systemd[1]: Failed to preset all unit: Unit /run/systemd/generator/sshd-generated@.service is transient or generated
2026-09-29T06:27:14+00:00 systemd[1]: Failed to preset all unit: Unit /run/systemd/generator/systemd-veritysetup@usr.service is transient or generated
```

---
_Filed by [Booty](https://github.com/jeefy/booty)'s autopilot. Created 2026-09-29T06:30:31Z, updated 2026-09-29T06:35:35Z. No hostnames, MAC or IP addresses are included._
````

Notes on that text: *"fail the health gate 4 times"* counts the healthy rollback attempt (fix 5 makes it *2 times (4 attempt(s) including the rollback)*), and the *journal errors* are **not** from the failing boot — a boot loop never reaches `booty-health.service`; they are the healthy 673 boot's harmless preset noise, copied from the host's latest health report (fix 4: the excerpt now comes only from a health report of an attempt into the blamed release, and the section says *the failing boot never got as far as a health report (`boot-loop`)*).

### What misbehaved, and the fixes (all in this PR, each with tests)

1. **Every first-boot gate failed `node-not-ready` under `--autopilotHealthWindow=6m`** (05:55–06:02). The L2 gate demanded 5 min of Ready stability inside a 6-min window: a node that takes 90 s to join can never pass (`node Ready for 4m47s, waiting for 5m0s of stability` at the window's end). Both workers went to attempt 2 through kured on the good release. Fix `8e4dd8b`: `NodeStable` defaults to `min(5m, window/2)`; 15 m keeps 5 m, the lab's 6 m gets 3 m. Restarting Booty for it while kured was mid-reboot left `w-bf1` in the iPXE shell (`/booty.ipxe` unreachable) → `hung` → NEEDS-HANDS on 673; I reset it from the QEMU monitor and, with both workers healthy on 673 again, deleted `state.json` and the pins (06:11:34Z) so the episode below starts from a clean `idle`. Lesson for the homelab: never restart Booty while a kured reboot is in flight, or run two.
2. **A non-canary host was rebooted when the bad release landed** (06:15). `current` moved to 674 at 06:14:10; the controller applied the `lastGood` hold on its next tick 21 s later; `w-bf2`'s update timer fired in between, `/update-check` said `rebootRequired` for 674, kured drained and rebooted it — into 673, since the hold was in place by then. Harmless here, but under `full` the fleet must never follow `current`. Fix `72203f7` + `f58aa43`: `versions.SerialRollout(os)`, set by the full-mode controller at start-up, makes `FleetHold` answer `lastGood` whenever `current` differs from it, synchronously with the release landing; `TestBluefinFullHoldIsImmediateOnRelease` covers the gap. Verified live at 06:48: 678 landed, `w-bf2` stayed on 673 until its turn.
3. **`/bluefin/<mac>/SHA256SUMS` followed the target, not the booting kernel** (06:21–06:26 and 06:35–06:41). The second boot-loop fetch that fails attempt 2 is also the first fetch of the next iteration; the controller pins `lastGood` in the same instant, so that iteration's initrd (674's kernel) asked for `SHA256SUMS` and got 673's, `systemd-pull` found no entry for `bluefin-server_26.09.674.raw`, the loop device never appeared and the initrd sat in `A start job is running for /dev/mapper/usr (… / 5min)` until `systemd.default_device_timeout_sec=300`, then Bluefin's emergency path rebooted it. The rollback kernel fetch came 5 m 51 s and 5 m 49 s after the request — 9 and 11 s inside the 6-min `hung` deadline; with `verify=signature` upstream and an initrd that stays in the emergency shell this is a NEEDS-HANDS. Fix `92ce6e2`: the host record gains `netbootVersion` (stamped on every real UKI or BIOS kernel fetch); `SHA256SUMS`, `SHA256SUMS.gpg` and the real `bluefin-node.ign` follow it while that release is cached (previews and never-netbooted hosts keep the target). `TestBluefinChecksumsFollowTheNetbootedRelease`.
4. **The report borrowed the healthy boot's journal** — fix `6d186c1`, above.
5. **Report summary counted the rollback as a failure** — fix `6e30bab`, above.

Not bugs, but worth knowing: Bluefin's netboot initrd reboots itself out of `emergency.target` (`Finished Emergency Shell` → `systemd-shutdown: Rebooting`), which is what turned a would-be hang into a slow loop; the BIOS path fetches ≈630 MB per loop iteration (kernel+initrd 90 MB, DDI 540 MB), so a boot loop on a 1 GbE fleet costs ≈50 s per turn as here; `verify=checksum` in the bad UKI is a lab necessity, not something Booty does — Booty never inspected or changed the `verify=` option.

### Not verified here

- **`guard` on a bad Flatcar release** (plan step "truncated initrd → first node downgraded, fleet held"): Booty only learns about Flatcar releases from the channel's `version.txt`; there is no mirror flag, and faking `data/flatcar/<v+1>/` by hand plus a pin would test the fixture, not the sync. Skipped rather than done hackily; the controller path is the same code the Bluefin episode exercised (`guard` is `full` minus the serial rollout) and is unit-tested (`TestGuardFullEpisode`).
- **Posting** to the scratch repository: this run used drafts on disk only; P4's live test against `jeefy/booty-autopilot-scratch` stands.
- **UEFI HTTP Boot** and the API/SSH actuators: the run was BIOS diskless with kured (the homelab's shape). The API actuator was `dryRun`-visible before kured existed (`actuator: api`, `Booty's image is unknown` warning) and was not exercised.
- The homelab `aren` step.
