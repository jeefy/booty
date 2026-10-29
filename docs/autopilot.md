# Fleet Status and Autopilot

Fleet status reporting and the self-healing upgrade controller: health gates, rollbacks, soak/cooldown pacing, actuators and reports.

## Fleet status

`GET /update-check?mac=&os=&version=&image=&digest=` is what `booty-update-check` calls every 10 minutes. It answers `{"rebootRequired":bool,"running":"...","target":"...","reason":"..."}`, and the rule is the same for every OS: **`rebootRequired` when the host's effective target release differs from what it reports running**, where the effective target is the host's `targetVersion` when set, else the fleet target (the OS's `current` release). Booty answers `false` while no release of that OS is cached or the host reported no version, so without a `targetVersion` the Flatcar and CoreOS answers are exactly what they were before per-host targets existed:

* Flatcar (`os=flatcar`, from `/etc/os-release`): compares the reported `VERSION_ID` with the target release (`flatcar <running> differs from served <target>`).
* OSTree systems (`os=coreos`, including Universal Blue images): `rpm-ostree status` supplies the running image reference and digest, recorded as `running`. A host registered with an `ostreeImage` is answered `false` with the reason `image hosts update through rpm-ostree; booty does not track image digests` (Booty no longer mirrors images, so it has nothing to compare the digest against; `rpm-ostree upgrade` on the host is the update path). Plain Fedora CoreOS without an image compares `OSTREE_VERSION` with the target CoreOS release.
* Bluefin Server (`os=bluefin` or `bluefin-server`, or a host registered as `bluefin`): compares `VERSION_ID` with the target release; a diskless host behind it gets `rebootRequired:true` with the reason `bluefin: re-image on reboot into <target>`, since a reboot re-images it from Booty into that release -- that is the whole upgrade and rollback mechanism. (Installed Bluefin hosts run no Booty Ignition and never ask.)
* Booty never says `true` when it cannot determine the target; 400/404 answers make the client leave the reboot flag alone.

Each check is recorded on the host (`running`, `lastCheck`, `rebootPending`, visible in `/booty.json`, `/hosts` and the UI), rewritten at most once a minute when nothing changed. `GET /info` gains `"fleet":{"hosts":N,"pendingReboots":N}` so you can see at a glance how many nodes are waiting on kured, and `"targets":{"flatcar","coreos","bluefin"}`, the fleet target per OS (`current`, or the release the autopilot holds the fleet at), which the Hosts page compares every host's `running` against (a ✓ on target, `behind <target>` otherwise). A host with no `running`, `lastCheck`, `booted` and `health` at all -- one that booted before the `update`/`health` builtin units existed, or with `--builtin=none` -- shows a muted **no agent yet** badge instead of an unknown version, and the Overview counts them (`4 hosts have no agent yet`): reboot it to pick up the current Ignition, after which it reports every 10 minutes. A host whose last check is older than 30 minutes shows its version with `last seen Xm ago`.

`GET /info` also carries an optional `syncErrors` field: when a release sync or scheduler (per OS, and the Secure Boot bundle) last failed, it lists `{error, at}` for each, and the Home page shows them as a warning so a stuck upstream (a mirror that stopped answering, a checksum that cannot be fetched) is visible without reading the log. The field is omitted while everything syncs cleanly.

## Autopilot

Booty is a self-healing upgrade controller: it rolls a release across the fleet, checks that each node comes back healthy, retries, downgrades to the last good release when it does not, holds the fleet there and, for Bluefin Server, reports the bad release upstream. The design, and the QEMU evidence of a bad Bluefin release being caught, rolled back, retried, quarantined and skipped (P5), are in [docs/plans/2026-09-28-autopilot.md](plans/2026-09-28-autopilot.md); the paced-rollout addition (soak, cooldown, waves) is in [docs/plans/2026-10-07-soak-cooldown.md](plans/2026-10-07-soak-cooldown.md). `--autopilot=off` (the default) starts none of it and changes nothing.

### Modes

| `--autopilot` | Does |
|---|---|
| `off` | Nothing. Releases, `targetVersion` and the health reports are still recorded, nothing acts on them. |
| `guard` | Every OS: the health gate on every reboot into a release the host has not passed it on, one retry, rollback to `lastGood`, the fleet target held at `lastGood` from the first failure, TIMEOUT and a single retry, quarantine, NEEDS-HANDS. Rollout order is unchanged (kured, fleet-wide). |
| `full` | `guard` plus, for Bluefin: the canary-serial rollout (one host at a time, canary first), the TIMEOUT retry on the canary, and skip-to-next once a release is quarantined. |

Both modes honour the soak and cooldown clocks below when they are set; `canary: true` hosts are exempt from both in either mode.

Clocks: `--autopilotHealthWindow` (default `15m`, from the observed reboot, not from when kured was asked), `--autopilotRetryAfter` (default `1h`, the TIMEOUT), `--autopilotSoak` (default `0`, how long a release must have been healthy on a canary before the rest of its OS may move to it) and `--autopilotCooldown` (default `0`, the minimum time between the starts of two rollout waves of the same OS). The controller reconciles every 30 s and persists its state to `data/autopilot/state.json` (atomic write), so a restart resumes gates in progress with their stored `t0` and re-applies the fleet holds.

### The health gate

An **episode** is the attempts of one host into one release; an **attempt** is one reboot. The gate reads the signals P1 already records, in the handlers that record them:

| Level | Signal | Where it comes from | Fails as |
|---|---|---|---|
| L0 *served* | kernel/UKI fetch: `/booty.ipxe` (Flatcar/CoreOS), the netboot UKI or its BIOS `.linux` section (Bluefin). Starts the gate clock `t0`. | `handleIPXERequest`, `recordBluefinNetboot` | a second fetch before the OS is up = `boot-loop` |
| L0 | Ignition fetch (`/ignition.json` wrapper, `bluefin-node.ign` GET) | `recordBoot`, `serveBluefinNodeConfig` | none 5 min after L0 = `no-ignition` |
| L1 *OS* | `POST /booted` (optionally `?running=`) and `POST /health` | `handleBootedRequest`, `handleHealthRequest` | `failedUnits` non-empty = `failed-units` (immediately) |
| L2 *cluster* | polled every 30 s through the k8s client: Node `Ready` and stable for 5 min (capped at half of `--autopilotHealthWindow`, so a short window stays passable), `nodeInfo.osImage` not naming another cached release, every DaemonSet pod Ready, no pod in `CrashLoopBackOff`/`ImagePullBackOff`/`Error`/`Failed` or `Pending` for over 5 min **that was not already so in the baseline** sample taken at `t0` | `k8s.Client.Sample` | at the end of the window: `node-not-ready` or `workloads-unhealthy` |
| — | nothing fetched within a window after the controller asked for a reboot (node NotReady, or an actuator rebooted it) | — | `hung` |
| — | the window elapsed before L1 | — | `timeout` |

Without a cluster client (or a host without a hostname) the gate is L1 only and says so in the episode note. Job pods, pods that were already unhealthy before the reboot, and Booty's own `booty-node-reboot-<node>` Pods (label `app.kubernetes.io/component=node-reboot`; the API actuator's reboot Pod legitimately ends `Failed` when the node reboots underneath it) never count.

**Only the host's own kernel fetch is L0.** `/booty.ipxe?mac=…` and the Bluefin UKI/`.linux` count as a boot (for the gate and for the [Power](power.md#power) tracker) only when the request comes from the host's recorded `ip`, or while Booty knows no IP for the host yet -- a first boot, which then teaches it the address. A `curl` of the same URL from a workstation is served as usual but starts no gate, changes no power state and leaves the host's IP alone (`?preview=1` never counts either). The rule is address-only, so two things follow: while a host has no IP any address passes, and a host whose DHCP lease changed has that boot's kernel fetch ignored until its Ignition fetch or `POST /booted` relearns the address (its next boot is gated normally; if it was mid-episode, see the re-gate below).

### State machine

```
R cached ─► fleet target = R (full + Bluefin: canary first, then one host at a time;
            everything else: kured order as today)
host reboots into R ─► health gate
   healthy ─► when every host of that OS that has booted is healthy on R: lastGood = R
   failed  ─► fleet target held at lastGood; attempt 2: reboot again (same target, host pinned to R)
      healthy ─► hold released, continue
      failed  ─► targetVersion = lastGood, reboot, gate
                  healthy on lastGood ─► the release is bad: hold stays, R enters TIMEOUT,
                                         report stub drafted
                  failed on lastGood  ─► NEEDS-HANDS (alert, left on lastGood, release not blamed)
R in TIMEOUT for --autopilotRetryAfter ─► retry once (full + Bluefin: on the canary;
                                          otherwise on the host that failed)
   healthy ─► release leaves TIMEOUT, rollout resumes
   failed  ─► R QUARANTINED, host back on lastGood; full + Bluefin: pick the newest cached
              release that is neither quarantined nor in TIMEOUT and start again, else stay
hung (nothing fetched within the window) ─► NEEDS-HANDS: no actuator can reach a machine
                                            that is not up, and there is no BMC
reboot requested but never seen, or NEEDS-HANDS, and the host reports /booted + /health
healthy on the episode's target ─► gate reopened on that episode (re-gate); it ends as usual
```

Per host the state is `idle`, `rolling` (reboot requested, not yet observed), `gating`, `retrying` (attempt 2 requested), `rolled-back` (on `lastGood`) or `needs-hands`, shown with the attempt and the last class on the host record (`autopilot` field), in `/hosts`, the Hosts table and the Autopilot page. Per release: `rolling`, `timeout`, `quarantined`, `good`, `skipped` (paced rollouts only, see [Soak and cooldown](#soak-and-cooldown)).

**A host that comes back healthy on its target re-gates itself.** While an episode is waiting for a reboot the controller never saw begin (`rolling`, `retrying`, `rolled-back`; the kernel fetch was missed because the host's address changed, or Booty was down) or sits in `needs-hands`, a `POST /booted` followed by a `POST /health` that names the episode's target as `running` with no failed units reopens the gate on that same episode: `t0` is that boot's kernel fetch when the controller noted one, else the `/booted`, the attempt keeps its number (a concluded `needs-hands` episode gets the next one), the event `host came back on <target>: re-gating` goes into the timeline, and `evaluateGates` finishes it exactly as usual -- `healthy` closes the episode, records `healthyOn` and lets `lastGood` advance; a rollback that passes still puts the release in TIMEOUT; a failure takes the usual next step. For `needs-hands` this is the "hands were applied" signal, so an operator who resets a hung machine and watches it come back Ready needs no *Clear*. A report with failed units, or one naming another release, changes nothing: the host stays flagged.

**The fleet hold** is how a bad release stops after one node: while a host is failing on the current release, or the release is in TIMEOUT/quarantined, `FleetTarget(os)` answers `lastGood` instead of `current`, so every render and `/update-check` for hosts without a `targetVersion` follow it (hosts that already booted the bad release are asked to reboot back). Under `full` the Bluefin fleet target is held at `lastGood` for the whole rollout and hosts move one at a time through a controller-set `targetVersion` (shown as *pinned*), cleared once `lastGood` advances. That hold is a policy of the release store, in force from the instant a new release lands, not something the controller's next tick applies: a host whose update timer fires in between still sees `lastGood`, so kured never reboots a host the rollout has not picked. The controller only ever clears pins it set.

**Canary order** (Bluefin, `full`): hosts with `canary: true` sorted by MAC, then workers by MAC, then control planes; installed Bluefin hosts never take part. `canary` is a host field (`/register`, the host form) and is accepted for every OS.

### Soak and cooldown

An OS is **paced** when `--autopilotSoak` or `--autopilotCooldown` is non-zero; both default to `0`, which is exactly the behaviour described above, and an unpaced OS runs none of what follows. Pacing splits the hosts of an OS in two: the **canaries** (the registered, not-excluded hosts with `canary: true`) keep following `current` -- the newest non-bad release -- exactly as today, exempt from both clocks, and the other hosts move in **waves**.

A release is **soaked** when any of these holds: the soak is `0`; the OS has no canaries (the controller warns once per release, since with nobody running it first the soak has nothing to measure; the cooldown still applies); or its `firstHealthyAt` -- stamped the first time any host passes the gate on it, persisted in `state.json` -- is at least `--autopilotSoak` in the past **and** at least one canary still counts as healthy on it (passing the gate once is not enough; the canary's latest health report must still name the release with no failed units, so a canary that fell over un-soaks it, and one that moved on to the next release leaves the release soaked only while another canary remains on it).

A **wave** is one pass of the non-canary hosts of an OS into one release: fleet-wide, not per host. The wave target is the **newest** soaked, non-bad cached release newer than `lastGood` -- skip-ahead, not the next in sequence, so three releases landing inside one cooldown still cost one reboot per host, and the releases the fleet never moved to are marked `skipped`. A new wave starts at most once per `--autopilotCooldown`, counted from the previous wave's start. Under `full` with Bluefin the wave moves one host at a time through the serial rollout machinery, shared with the canary; everywhere else the controller pins every eligible host of the wave at once and kured (or the host's own update timer) orders the reboots. A wave ends `done` when no host is eligible for it any more, and `aborted` -- its pinned hosts fall back to the held fleet target and roll back through the usual machinery -- when its release goes bad mid-wave. Retries, rollbacks, TIMEOUT retries, operator `targetVersion` pins and the Power buttons ignore both clocks.

Example, with the homelab's `--autopilotSoak=24h --autopilotCooldown=48h` and two upstream releases a day: a release lands, the canary reboots into it and passes the gate; 24 h later it is soaked, but the cooldown from the last wave's start still has a day to run; at the 48 h mark a wave starts into the **newest** release that has soaked -- typically not the one that started the wait -- and every other host reboots once. One reboot per host per 48 h, however many releases landed in between.

Pacing relaxes one rule of the state machine: `lastGood` counts a **canary** as healthy on a release when it is on that release *or a newer one that is not bad*, because a canary already on the next release would otherwise hold `lastGood` back forever. Non-canaries must still be on the release exactly.

Releases the fleet skipped over become `skipped` when `lastGood` advances past them (a `rolling` record, older than the new `lastGood`, not bad, with no episode in flight), with the event `release skipped: the fleet moved to <v> without it`. `skipped` is a resting state like `good`: it folds into the history the same way, and it does not blame the release -- an operator pin can still target it.

Events (kind `release` or `fleet`): `release soaked: healthy on a canary for <soak>; eligible for the next wave` (once per release), `no canary registered for <os>; --autopilotSoak has no effect on it` (once per release), `wave started into <R> (<n> host(s)); next wave not before <t>`, `wave done: every host is on <R>` / `wave aborted: <R> is <timeout|quarantined>`, and the `release skipped` line above.

### Actuators

Chosen at each use, in this order: **kured** if a kured DaemonSet exists (Booty does nothing itself: `/update-check` answers `rebootRequired:true` while the host's target differs from what it runs, *and* while the controller is waiting for it to reboot into the same release again, so attempt 2 works), else **SSH** when `--rebootSSHKey` is set, else the **Kubernetes API** (cordon, evict, a privileged reboot Pod from Booty's own image; uncordon after the gate passes) as the last resort. An operator who hands Booty an SSH key has chosen how nodes are rebooted; the hostPID reboot Pod is for when neither kured nor a key exists. With no actuator at all the controller still gates, holds and downgrades through `targetVersion`, logs that it cannot force a reboot, and waits for the host's own update timer. Actuators are called from the controller only; a boot loop supplies its own next attempt. Details in [Cluster awareness and actuators](#cluster-awareness-and-actuators-p2). The operator's [Power](power.md#power) buttons use the same SSH and API actuators (and their `PowerOff`) but never kured.

### Endpoints and UI

* `GET /autopilot`: `mode`, `actuator`, `cluster` (from P2), `dryRun` (`true` only when off), `healthWindow`, `retryAfter`, `soak` and `cooldown` (duration strings, present when non-zero), `revision` (changes whenever the controller's state did; the UI polls every 30 s and re-renders only when it moved), per-OS `os.<name>: {fleetTarget, current, lastGood, held, wave:{release, startedAt, endedAt, outcome} (omitted when there never was one), nextWaveAt (only while the cooldown blocks the next wave), canaries (count), releases:[{version, state, since, attempts, class, report, healthy, failedOn, failing, cached, firstHealthyAt, soaked}], releaseCount}` with release state now also `skipped`, per-host `hosts:[{mac, os, healthyOn, pinned, episode}]` with the episode's attempts (the newest 10) and relative timeline, `events` and `eventCount` (the text never carries hostnames, IPs or MACs, the MAC is a field), `reports:[{key, os, version, lastGood, draft, createdAt, updatedAt, class, attempts, rollbackResult, path, postedURL, postedAt, postAction, postError}]` (see [Reports](#reports-p4)), `held`, `quarantined`, `needsHands`. The answer is bounded by default: `releases` lists the live records (cached, or in any state but `good`) plus the 10 newest `good` ones whose files are gone, `releaseCount` says how many records there are in all, and `events` is the newest 100 of the ring of 200 (`eventCount` its size); `?releases=all` lists every record (the Storage page's *Show pruned release history* uses it) and `?events=all` the whole ring. `cached:false` marks a record whose files are gone, see [Storage](storage.md#storage) and [Release history](#release-history) for what the controller keeps of it.
* `GET /autopilot/reports/<os>-<version>.md` serves a rendered report as `text/markdown` (`.json` for its JSON twin); anything that is not a well-formed report key is a 404.
* `POST /autopilot/{os}/release/{version}/clear` un-quarantines (or ends the TIMEOUT of) a release so the fleet tries it again; like `/register` it needs nothing but reach to the port (see [Trust model](trust-model.md#trust-model)). `409` when the release is not in a state to clear.
* `POST /autopilot/host/{mac}/clear` is the operator's acknowledgement of one host's episode: a host stuck in `needs-hands` that you are not going to bring back on its target (one that comes back healthy on it re-gates itself, see [State machine](#state-machine)), or a `retrying`/`rolled-back` episode you do not want to run (the controller is about to reboot the host again, and it is fine). The episode ends (`done`, state `idle`; the attempts and the last class stay on the record), the `targetVersion` the controller pinned is cleared (operator pins are left), `/update-check` stops asking for the reboot, the release stops counting the host as failing and the fleet hold is recomputed -- so a hold caused only by this host's retry goes, while a release in TIMEOUT or quarantine keeps the fleet at `lastGood` until its own clear. The release itself gets no verdict: the host is not recorded healthy on it, and its next boot of that release is gated afresh. The node is left as the actuator left it (cordoned, when the API or SSH actuator drained it before the reboot; `kubectl uncordon` it yourself), and under `full` a cleared Bluefin host is eligible for the rollout again. Answers `{"status":"ok","mac":...}`, `400` for a malformed MAC, `404` for an unregistered one, `409` when the host has no episode to clear (idle, or never had one) or the autopilot is off. The event `episode cleared by the operator while <state>` goes into the timeline.
* `GET /info` gains `"autopilot":{"mode","actuator","held":[os…],"quarantined":n,"needsHands":n}`.
* The **Autopilot** page (polled every 30 s; an answer with the same `revision` re-renders nothing) shows mode, actuator, cluster and attention counters, the soak and cooldown clocks in the header next to the health window when set, the fleet target/`current`/`lastGood` per OS with the hold, a **Wave** line on each OS card (`wave into 26.10.880 · 2/3 hosts · started 3 h ago`, `last wave done 20 h ago · next wave in 28 h`, or `aborted: 26.10.880 quarantined`), the releases that need an eye on them (rolling, failing, TIMEOUT, quarantined) with their state and since when, a `soaking 13h / 24h` badge on a rolling release that has not soaked yet and `soaked` once it has, the OS's `good` releases behind a *Show N good releases* toggle (`skipped` folds in with them, rendered muted like `good`; a record whose files were pruned is muted with a *files pruned* link to the Storage page, unless it is quarantined or in TIMEOUT) and an *N more in history → Storage* link for the records the default answer leaves out, the per-host state table (attempt, class, note), the reports (draft/final badge, *View* opens the redacted Markdown inline, `.md` opens it raw, and the GitHub issue link once posted) and the event timeline, 50 events at a time: *Show more* goes to 100, then fetches the whole ring with `?events=all`. A *Clear* button sits on quarantined/TIMEOUT releases and, behind an inline confirm, on `needs-hands`, `retrying` and `rolled-back` host rows (`POST /autopilot/host/{mac}/clear`). The host form has the *Autopilot canary* checkbox and the Hosts table shows `canary` and the episode state as badges.
* `alert` events (NEEDS-HANDS, failed actuator writes) are also logged at error level.

### Release history

The controller keeps a record of every release it has seen, because the verdict is the history: a quarantined release stays quarantined after its files are pruned, and the Storage page can show what was tried. With one or two upstream Bluefin releases a day that history would grow without bound, so `state.json` is compacted on every reconcile tick (only when the record list, the cached list, `current` or `lastGood` changed; a quiet tick costs a string compare):

| Record | Kept as |
|---|---|
| `quarantined` or `timeout`, cached or not | intact, forever: the hosts that passed on it, the host that blamed it, the report. These keep hosts off the release and are never folded or dropped. |
| `rolling`, `failing`, `current`, `lastGood`, or any record whose files are still cached | intact |
| `good`, files pruned, never through TIMEOUT or quarantine (no report) | **folded**: `version`, `state`, `since`, `attempts` and `class` stay; the per-host MAC lists (`healthy`, `failedOn`) go |
| `good`, files pruned, with a report (it went through TIMEOUT once and recovered) | intact; counts toward the cap below but is never dropped |
| `skipped` | treated like `good`: foldable, counts toward the cap, listed with the good history rather than as live |

Per OS, at most **50** `good` records without files are kept; beyond that the oldest folded ones (by `since`) are dropped, with a `release` event (`release history compacted: N oldest good record(s) without files dropped (50 kept)`). That is the only deletion the controller ever does to its history. Episodes keep their newest 10 attempts, each attempt's relative timeline at most 64 lines, and a journal excerpt at most 50 lines of 300 bytes (bounded on the way in by `POST /health`). `GET /autopilot` is bounded independently of what is kept, see [Endpoints and UI](#endpoints-and-ui).

**Safety.** Never more than one Bluefin host is rolling under `full`; under `guard` the rollout order is not changed, only held. `lastGood` only advances when every host of the OS that has ever booted is healthy on the release. A host that fails on `lastGood` too is a sick node, not a bad release: it is left alone and flagged, the release is not blamed and the hold released. Nothing in the controller writes to the cluster except through an actuator; the tests assert the fake API server sees GETs only.

### Reports (P4)

For every release the autopilot blames, the controller keeps a **report stub** in `state.json` (attempts with classes and relative timelines, DMI vendor/product/BIOS, firmware, kernel, boot path, kubelet/osImage/runtime from the node, the rollback result, the raw journal excerpt from the failing boot's health report, and a hash of the DMI product UUID) and renders it through `pkg/autopilot/report` to **`data/autopilot/reports/<os>-<version>.md` and `.json`** (atomic writes) every time the stub changes: drafted when the release enters TIMEOUT, final when it is quarantined. Files that go missing are regenerated at start-up from the stub. The Markdown carries the OS and release, `lastGood`, the boot path (`bios-diskless`, `uefi-http`, `uefi-pxe`, `bios-pxe` or `unknown`, derived from how Booty served the host and the firmware its health report named), hardware, kernel, kubelet/Kubernetes version and `osImage`, the CNI name and pin when Booty installed it (`--controlPlane=managed`; omitted otherwise), the failure class of every attempt, the failed unit names, the relative boot timeline (`t+0s kernel fetched`, `t+38s ignition fetched`, …), the attempt count, the rollback result ("lastGood X healthy on the same hardware"), the redacted journal excerpt and the hidden dedupe marker `<!-- booty-autopilot: <os> <version> <dmi-hash> -->` (`dmi-hash` = first 8 bytes of sha256 of the DMI product UUID, so two machines can be told apart without naming either).

**Redaction** (`report.Redact`, unit-tested rule by rule; the fixture report is laced with hostnames, MACs, IPs, UUIDs, a `/bluefin/<mac>/` URL, an SSH key and a token, and the test greps both files for `([0-9a-f]{2}:){5}|[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+|<hostnames>|[0-9a-f]{8}-[0-9a-f]{4}-` = 0 matches):

| What | Becomes |
|---|---|
| `/bluefin/<mac>/` in URLs (colons, dashes or `%3A`) | `/bluefin/<host>/` |
| UUIDs (DMI `productUUID`, `bootID`, anything 8-4-4-4-12) | `<uuid>` |
| MACs (`:`, `-` or `%3A` separated, any case), incl. `mac=<mac>` query values | `<mac>` |
| IPv4 (with optional `/prefix`), IPv6 (full or `::`-compressed, `%zone`, `[bracketed]`, `::ffff:`-mapped) | `<ip>` |
| SSH key blobs (`AAAA` + 40 or more base64 chars) | `<key>` |
| GitHub tokens (`ghp_`, `gho_`, `github_pat_`, …), `Bearer …`, `token=`/`password=`/`secret=` values, `--token <v>`, `--token-file=<v>` | `<token>` |
| Every whole-word occurrence of a registered host's hostname (the node name), case-insensitive; `n1.lan` → `<host>.lan` | `<host>` |

Times such as `10:00:01` and dotted versions such as `4757.2.0` are left alone; everything not listed is kept verbatim. The hostname field of journal lines is already dropped on the node (`journalctl --no-hostname`). The raw excerpt stays in `state.json` only, never under `reports/`.

**Posting** (Bluefin only): `--autopilotIssues=<owner>/<repo>` with `--githubToken` (a token with `issues:write` on that repository; the same token still raises the releases-API limit) files the final report as a GitHub issue the moment a Bluefin release is **quarantined** — never at the TIMEOUT draft, never for Flatcar or CoreOS (their reports stay on disk). The flow, in `pkg/autopilot/report/github.go`:

1. Search open and closed issues for the release's marker (`GET /search/issues?q=repo:… is:issue "booty-autopilot: bluefin <version>" in:body`); when the search API is rate-limited or refused, list the issues labelled `autopilot` instead and read their bodies.
2. **None** → `POST /repos/<owner>/<repo>/issues` with the title `Bluefin Server <version>: <class> on <vendor> <product> (autopilot)` (behind `--autopilotIssueTitlePrefix`, verbatim, when set: `[autopilot test] Bluefin Server 26.10.868: …`), the label `autopilot` (created on a 404; failure to create or apply it is tolerated) and the Markdown as the body.
3. **One exists, its body or comments carry this machine's marker** (same release, same DMI hash) → nothing: already reported.
4. **One exists from another machine** → `POST …/issues/<n>/comments`: "Another machine (dmi-hash …) hit this: `<class>`, N attempts …" (the same prefix in front of it) with this machine's marker, the failed units and the redacted excerpt in a `<details>` block.

Every API call gets three tries with exponential backoff (1 s, 2 s) on network errors, 5xx and rate limits; the search call falls back instead of waiting. Posting runs in a goroutine and never blocks the controller: the outcome is a `report` event, `report-posted <url> (created|commented|already-reported)` or `report-post-failed <status> (…)`, the issue URL lands on the report (`postedURL`, shown as a link on the Autopilot page) and a failed post is retried on the reconcile tick an hour later (`postError` in the meantime). The tests run the poster against an `httptest` GitHub API: create, comment, already-reported, the listing fallback, 5xx retries, a 401/403 that becomes an event without a panic, and the title prefix. **Test on a fork or a scratch repository first**: to test against your own repo, set `--autopilotIssues=you/your-repo --autopilotIssueTitlePrefix='[autopilot test] '` -- the prefix goes in front of every issue title and comment header so the scratch issues cannot be mistaken for real reports, while the hidden dedupe marker stays the same. The report is public the moment it is filed, and while the redactor is thorough it is a regex, not a promise. Move to `projectbluefin/server` (without the prefix) once you have read a few of your own reports.

### Health reports (P1)

`POST /health?mac=<mac>` is what the `health` builtin's `booty-health.service` calls once a node reaches `multi-user.target` (on Flatcar, CoreOS and diskless Bluefin alike; see [Composition](ignition.md#composition) for the body). Registered hosts only (404 otherwise); the body is capped at 64 KiB and every field is trimmed and bounded server-side (at most 200 failed units, 50 journal lines of 300 bytes within 8 KiB, `firmware` must be `uefi` or `bios`). The report is stored as the host's `health` field with a `receivedAt` stamp and shows in `/hosts`, `/booty.json` and the UI:

```json
"health":{"receivedAt":"2026-09-28T12:00:00Z","bootID":"5b1c…","running":"26.09.673",
          "failedUnits":["booty-kubeadm-join.service"],"journalErrors":["2026-09-28T10:00:01+0000 kernel: …"],
          "dmi":{"vendor":"HP","product":"HP EliteDesk 800 G1 DM","biosVersion":"L01 v02.78"},
          "firmware":"uefi","kernel":"6.17.1-300.fc44.x86_64"}
```

A report replaces the previous one; a repeated POST from the same boot (same `bootID`) converges on the same stored state and is logged at debug level only. It sets the host's `running` when the update check has not yet. Journal lines may carry hostnames and addresses, so Booty logs them at debug level only -- the info-level log has counts, the release, the firmware and the kernel.

### Cluster awareness and actuators (P2)

With `--autopilot=guard|full` Booty connects to the cluster, detects kured and chooses the actuator the controller uses; `off` skips all of it (`/autopilot` answers `{"mode":"off","actuator":"none","dryRun":true}`).

**Cluster client.** The same plain `net/http` client the token minter uses (in-cluster service account, or `--kubeconfig`; `--controlPlane=managed` shares the minter's admin certificate), extended to read nodes (Ready condition and its transition time, `spec.unschedulable`, the control-plane label, `nodeInfo`: `systemUUID`, kubelet, OS image, kernel, runtime, kured's `weave.works/kured-*` annotations), the pods on a node across all namespaces (phase, ready/restart counts, controlling owner, mirror pods) and the DaemonSets. Failures are typed: unreachable, forbidden (RBAC), not found. On it sits the **health-gate primitive** the controller will poll every 30 s: a node is healthy when it is `Ready`, every pod on it that is not a Job's is `Running` with all containers ready (or `Succeeded`), and no container restarted since the baseline sample taken before the reboot.

**Actuators**, chosen in this order each time one is needed:

1. **kured** -- a DaemonSet named `kured`, or labelled `app=kured` / `app.kubernetes.io/name=kured`, in any namespace (detection cached 5 min). Booty does nothing itself: `/update-check` already answers `rebootRequired:true` while the host's target differs from what it runs, the node's `booty-update.timer` touches `/var/run/reboot-required`, and kured drains, reboots and uncordons. Booty never touches kured's lock.
2. **SSH** -- `--rebootSSHKey=<private key>`: `sudo systemctl reboot` as `core` on every OS (Booty provisions that user on Flatcar, CoreOS and Bluefin; Bluefin ships root locked since 26.10, see [The node's Ignition config](bluefin.md#the-nodes-ignition-config)). Host keys are pinned on first use into `--dataDir/autopilot/known_hosts` and must match afterwards. The drain goes through the API when a client exists, else there is none.
3. **Kubernetes API** (the last resort, when neither kured nor a key exists) -- cordon (`PATCH nodes/<n>` `spec.unschedulable`), evict every pod that is neither a DaemonSet's nor a mirror pod through `pods/eviction` (PodDisruptionBudget refusals are retried with their `Retry-After` up to `--autopilotDrainTimeout`, default 10 m), then a **reboot Pod** on the node: Booty's own image (`--autopilotImage`, default the `BOOTY_IMAGE` environment variable; refused with a clear error when unknown), `hostPID: true`, privileged, `nodeName` pinned, tolerating every taint, `restartPolicy: Never`, `activeDeadlineSeconds: 120`, named `booty-node-reboot-<node>` (labels `app.kubernetes.io/name=booty`, `app.kubernetes.io/component=node-reboot`; the previous one is deleted first), in `--autopilotNamespace` (default Booty's own from `POD_NAMESPACE`, else `kube-system`). It runs **`booty node-reboot`**: `sync`, then `SIGRTMIN+5` to the host's PID 1, which makes systemd start `reboot.target`. The Pod necessarily ends `Failed`/`Error` -- the node goes down underneath it -- so the health gate never counts it as a workload. Uncordon once the node passes the health gate (or reports `/booted` after an operator reboot), and delete the Pod at the same time so it does not linger.

The node name is the host's `hostname` (both OS paths set it from Booty); when the host's health report carries a DMI `productUUID` (the `health` builtin now sends `/sys/class/dmi/id/product_uuid`, root-readable, stored in `health.dmi.productUUID`) it must equal the node's `nodeInfo.systemUUID`, or the actuator refuses. Like the MAC, it is an identifier and never enters a report.

**RBAC** for the API paths ([examples/k8s.yaml](../examples/k8s.yaml)): a `ClusterRole` with `nodes` get/list/watch/patch, `pods` get/list/watch, `pods/eviction` create and `apps/daemonsets` get/list/watch, and a `Role` in Booty's namespace with `pods` create/delete/get for the reboot Pods. The Deployment also sets `POD_NAMESPACE` (downward API) and `BOOTY_IMAGE` (a literal; keep it equal to the container image, the downward API cannot supply it).

The cluster block of `GET /autopilot`:

```json
{"mode":"guard","cluster":{"reachable":true,"kured":true,"nodes":6,"apiServer":"https://..."},"actuator":"kured","dryRun":false, ...}
```

`cluster.reachable` is whether the node list succeeded (a 403 still counts as reachable and carries the RBAC error in `cluster.error`; a dead server does not), `actuator` is `kured`, `api`, `ssh` or `none`, and `sshUsers` names the per-OS logins when it is `ssh`. The only code that builds an eviction or a reboot Pod is behind the actuator methods, and only the controller calls them (the test suite asserts `GET /autopilot` and the controller's own polling issue nothing but GETs against a fake API server).
