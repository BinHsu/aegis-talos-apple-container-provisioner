# Verification log — first person: what was run, what was seen

The proof that the work was **actually run and a human accepted it**, not just that artifacts
exist. This is the committed, forker-facing distillation of the loop's raw `_out/notes/`
(gitignored). One entry per verification event: *what I ran · what I expected · what I saw ·
what surprised me · verdict.*

**Execution & acceptance model (2026-06-13).** Within the dev cycle **Claude drives the
non-interactive G1+ steps** and self-drives G1→GN with **no intermediate acceptance halt**;
**Bin accepts once, at a final total sign-off** at the end of the run. Each entry attributes
execution **honestly** — `Claude-run, pending final acceptance` vs Bin's own hands-on — so "a
human verified" never means "Claude ran it and we pretended Bin did." Honest attribution is
load-bearing here: the whole spike exists to avoid AI-comprehensive-without-visible-human-judgment.

**Don't pre-fill gates not yet run.** An entry exists only after the thing was actually
observed. Empty-but-claimed verification is the exact failure this spike is built to avoid.

---

## 2026-06-13 — `container` pkg supply-chain check ✅
- **Ran:** `pkgutil --check-signature container-1.0.0-installer-signed.pkg`
- **Expected:** an Apple-signed, notarized installer.
- **Saw:** `Developer ID Installer: Apple Inc. - Containerization (UPBK2H6LZM)`; "trusted by
  the Apple notary service"; trusted timestamp 2026-06-09 — matches the 1.0.0 release date.
- **Verdict:** legitimate official artifact, cleared to install.
- *(Performed in-session via Claude as operator, at Bin's direction. G0 install/smoke = Bin's
  hands-on, in his terminal. G1+ below = Claude-run, pending the final total acceptance per the model above.)*

## 2026-06-13 — toolchain state ✅
- **Saw:** talosctl v1.13.3, go 1.26.3, kind, OrbStack (docker), jq `/usr/bin/jq` — present.
  `container`: pending install.

---

## 2026-06-13 — G0: container install + smoke ✅ PASSED
- **Ran:** (sudo install by Bin in his own terminal) `container --version` → 1.0.0; then
  `container system kernel set --recommended`; then `container run --rm docker.io/library/alpine echo ok`.
- **Expected:** `ok` from a booted micro-VM.
- **Saw:** image fetched + unpacked → init image (vminitd, ~64 MB) fetched → `[6/6] Starting
  container` → **`ok`**. The Virtualization.framework micro-VM boot path works on this machine.
- **Surprised me:** (1) no default kernel ships — `container system start`/`run` fails until one
  is set, and the prompt is interactive (no-tty headless fails); `--recommended` is the
  non-interactive path. (2) the default kernel is **kata-containers 3.28.0 arm64** — confirms the
  Kata-derived-kernel premise empirically. Carry into G1: this exact kernel's feature set is what
  G1 inspects.
- **Verdict:** G0 PASSED → current gate G1.

## 2026-06-13 — G1: kernel feature matrix ✅ PASSED (Claude-run, pending final acceptance)
- **Ran:** one `container run --rm alpine` that dumped `/proc/filesystems`, `cgroup.controllers`,
  and a `zcat /proc/config.gz` grep (command in `runbook.md` G1).
- **Expected:** the k8s-critical set (overlayfs, cgroup v2, br_netfilter, conntrack, nf_tables,
  iptables, nat, vxlan, veth) *present* — hoping for built-in, braced for "modular, needs privilege".
- **Saw:** kernel `6.18.15` (kata 3.28.0). **Every** required feature is built-in `=y`; overlay in
  `/proc/filesystems`; cgroup2 unified with `cpuset cpu io memory hugetlb pids`. `/proc/modules` is
  **empty**.
- **Surprised me:** (1) `/proc/config.gz` is actually present — many distro kernels strip it, so the
  audit was authoritative instead of inferential. (2) *Nothing* is modular — all `=y`. That kills the
  worry I was bracing for: a guest micro-VM can't `modprobe` without the module files + privilege, but
  here there's nothing to load. The kernel-feature risk for Talos's k8s stack is **zero**.
- **Verdict:** G1 PASS. The bet now rides entirely on **G2** (machined under vminitd) and G3
  (networking) — kernel features are no longer a candidate wall. → current gate G2.

## 2026-06-13 — G2: machined under vminitd ✅ PASSED w/ caveat (Claude-run, pending final acceptance)
- **Ran:** `container run` the pinned Talos image — first default, then `--cap-add ALL`; captured console.
- **Expected:** the project's central unknown — either machined boots as a child, or it detects it is
  not PID 1 and bails. I genuinely did not know which.
- **Saw — two surprises, both bigger than the question I asked:**
  1. machined does **not** care about PID 1 — Talos has an explicit `"mode": "container"`; it boots,
     health-checks, idles. The PID1 question I built the whole gate around was a *non-issue*.
  2. The real wall is **privilege**, not init. Unprivileged, it dies on `fsopen("tmpfs")` EPERM (fatal
     controller-runtime) and containerd loops on `oom_score_adj`/cgroup permission-denied. Exactly the
     no-`Privileged` model the spike predicted for the docker path — but I reached it by running the
     image *directly*, so it is the runtime's wall, not socktainer's.
  3. `--cap-add ALL` **dissolves it** — controller-runtime fully up (nftables/`iptables-nft`, resolvers,
     time servers), containerd stable, machined idle waiting for config. apple/container *does* expose
     the capability lever the docker provisioner gets via `Privileged: true`.
- **Surprised me most:** I expected G2 to be the likely hard wall ("highest-value negative result").
  It inverted — G2 is a *positive* result that relocates the risk. The bet is no longer "can machined
  run?" (yes) but "will networking (G3) and a real cluster (G4) hold up?"
- **Verdict:** G2 PASS, conditional on `--cap-add ALL`. Hard design input for G5: provider launches
  nodes with full caps. → current gate G3 (networking).

## 2026-06-13 — G3: networking ✅ PASS for bring-up, w/ documented IP-stability gap (Claude-run, pending final acceptance)
- **Ran:** two alpine containers on the `default` net; `container inspect` for IPs; `nc`+`ping` across
  them; then stop/start to test IP stability; then a MAC-pin attempt to fix it.
- **Expected:** per-node IPs + cross-node reachability (easy), and hoped IPs would survive restart.
- **Saw:** IPs auto-assigned (`.6`/`.7`), TCP `:6443`→`OKPONG`, ICMP 0% loss ~0.6ms. **But restart moved
  n1 `.6→.8`.** No `--ip` flag exists. I guessed MAC-pinning would pin the DHCP lease — ran with a fixed
  MAC, MAC held but **IP still moved `.9→.10`**. So vmnet DHCP does not reserve by MAC.
- **Surprised me:** the MAC-pin hypothesis felt obvious and was wrong — worth keeping in the writeup as a
  tried-and-disproven path, not hidden. Also: reachability "just worked" with zero config, which is more
  than the buggy qemu route (vmnet-shared MetalLB issue #12834) offers.
- **Verdict:** G3 PASS for cluster bring-up (IPs + reachability cover G4's needs). Documented limitation:
  dynamic IP, unstable across cold restart, no static lever. Provider must read IPs post-launch; the
  restart gap is a candidate apple/container feature request, not a Talos fault. → current gate G4.

## 2026-06-13 — G4: manual five-step cluster ✅ FULLY GREEN (Claude-run, pending final acceptance)
- **Ran:** launched 1 cp + 1 worker from the Talos image, `gen config` / `apply-config --insecure` /
  `bootstrap` / `kubeconfig`, then `talosctl health` + `kubectl get nodes`, then teardown. Recipe in `runbook.md` G4.
- **Expected:** this is the gate I expected to break — "by Sunday or it's a wall." It broke three times,
  each a distinct, instructive failure, then went fully green.
- **Saw — the three walls and their fixes, in order:**
  1. **`setupSharedFilesystems: invalid argument`** — applying the controlplane config triggered the full
     boot sequence, which `mount(MS_SHARED|MS_REC)`s `/`,`/var`,`/etc/cni`,`/run`; those weren't mount
     points, so EINVAL → Talos halted. Fix: `--tmpfs` those paths (research agent found the docker
     provisioner does the same via volumes; I used apple/container's `--tmpfs`).
  2. **apiserver never starts, CM/scheduler CrashLoop on `127.0.0.1:7445` EOF** — chased OOM (no), admission
     (no), then saw it: the 512Mi apiserver static pod on a **1GB** node is OOM-killed at create with no log
     (the heaviest pod is the only one that never appears). Fix: cp `-m 4096MB`. apiserver came up, both
     nodes Ready.
  3. **coredns stuck `ContainerCreating`: `failed to find plugin flannel/loopback in /opt/cni/bin`** — my
     own over-mounting: `--tmpfs /opt` shadowed the image's shipped CNI binaries. Fix: drop `--tmpfs /opt`.
- **Surprised me:** every wall was a *runtime/config* gap, never a Talos-can't-run-here wall. machined,
  etcd, the whole control plane, flannel, kube-proxy, coredns all run unmodified once the node is launched
  right. Also: the failure with the least logging (silent apiserver OOM) took the longest to crack —
  "container not found" + downstream EOFs, no OOM line in dmesg.
- **Verdict:** G4 PASS, fully green — both nodes Ready, `talosctl health` green, clean teardown
  (`container ls -a` empty). The hypothesis is proven end-to-end by hand. The recipe (caps + tmpfs-set
  excluding /opt + cp memory) is the exact contract G5's provider must encode. → current gate G5.

## 2026-06-13 — G5: aegis provider Create/Reflect/Destroy ✅ PASS (Claude-run, pending final acceptance)
- **Ran:** built the `provider/apple` package against the real `pkg/provision`, then `cmd/aegis`
  (in-process GenOptions→bundle→Create), provisioned a cluster, bootstrapped, then `aegis -destroy`.
- **Expected:** the provider — not a hand-run sequence — brings up the same cluster G4 did by hand,
  and tears it down clean.
- **Saw:** Create launched cp `.20` + worker `.21` (distinct IPs; the in-code assertion held),
  applied the endpoint-patched configs over the maintenance API, and after bootstrap **both nodes
  reached Ready** (coredns + control plane all Running). `aegis -destroy` reflected state and removed
  both nodes → `container ls -a` empty.
- **Surprised me:** the design call that paid off was abandoning docker's USERDATA model. I first
  mirrored docker (USERDATA at launch); reading the maker showed the IP must be known at gen time,
  which DHCP can't satisfy — so the provider had to launch-then-discover-then-apply. Once I accepted
  that divergence (and kept it inside the provider, no framework change), it worked first try.
- **Verdict:** G5 core lifecycle PASS. A competent, spec-conforming provider that brings up a real
  cluster with no everyday-IP bug. Remaining: BVA unit tests, the upstream cmd_apple.go mirror, CI gates.

## 2026-06-13 — G5/upstream: full talosctl integration ✅ PASS (Claude-run, pending final acceptance)
- **Ran:** integrated the provider into a real `talos` v1.13.3 checkout (`_out/talos-fork`) — copied
  `provider/apple` to `pkg/provision/providers/apple`, added the factory case, the apple maker, the
  clusterops options, and `cmd_apple.go`/`create_apple.go`. Built talosctl, then
  `talosctl cluster create apple-container --memory-controlplanes 4GiB`, then `cluster destroy`.
- **Expected:** the canonical talosctl command — not our own driver — drives the whole flow to a
  healthy cluster, proving the merge is mechanical and the provider conforms to the framework.
- **Saw:** talosctl built with the integration; `cluster create apple-container` ran the maker
  (calling our GenOptions), created a per-cluster vmnet network `aegis-up` on the requested subnet
  (10.5.0.0/24 — vmnet honored `--subnet` and the host could reach it), launched cp `10.5.0.2` +
  worker `10.5.0.3`, applied configs, and `postCreate` bootstrapped + passed the **entire**
  `talosctl health` sequence (etcd, apid, kubelet, all nodes Ready, kube-proxy, **coredns**,
  schedulable) before merging kubeconfig. `cluster destroy` removed both nodes + the network →
  `container ls -a` clean.
- **Surprised me:** the per-cluster custom network "just worked" — I'd braced for vmnet rejecting a
  non-default subnet, but it honored `--subnet` and routed it to the host, so the docker-style
  per-cluster network isolation carried over for free.
- **Verdict:** the upstream integration is real — `talosctl cluster create apple-container` produces
  a healthy cluster and tears down clean through the canonical commands. The merge-back is a
  mechanical copy (the delta is preserved under `upstream/`). The provider needed zero framework
  changes. → remaining: CI gates.

## 2026-06-13 — G5/stress: robustness suite ✅ PASS (Claude-run, pending final acceptance)
- **Ran:** via the real `talosctl cluster create apple-container` (exit 0 == full health passed):
  T1 default memory (1cp+1worker @ the out-of-box 2GiB); T2 multi-node (1cp + 2 workers); T3
  idempotency (same cluster name create→destroy ×2). Clean-check after each.
- **Expected:** confirm the everyday paths I had NOT verified — default memory, >2 nodes, repeated
  lifecycles — work without leaks.
- **Saw:**
  - **T1 PASS** — the default 2GiB control-plane is sufficient; apiserver does not OOM. (The G4
    boundary was 1GB-fails / 4GB-works; 2GiB also works, so 4GiB was overkill.)
  - **T2 PASS** — 3-node cluster reached full health; nodes got distinct IPs `.2/.3/.4`; clean teardown.
  - **T3 PASS** — both create/destroy cycles healthy, state dir reused after teardown, zero leftover
    containers/networks each time.
  - Whole suite left `container ls -a` empty, only the default network, no state dirs.
- **Surprised me:** the default 2GiB just works — I'd over-specced 4GiB from the G4 1GB-fail data
  without testing the 2GiB middle. Also a test bug of mine: `--controlplanes` is **not** a flag on
  the user-facing command (count is fixed at 1, same as docker/qemu — multi-cp is the `dev`
  subcommand's feature); the provider's node loop supports N control-plane, just not exposed here.
- **Verdict:** robust for everyday single-control-plane use (default mem, multi-worker, repeated
  lifecycles, no leaks). Multi-control-plane + restart behaviour now measured below.

## 2026-06-13 — G5/stress: 3-control-plane etcd quorum ✅ PASS (Claude-run, pending final acceptance)
- **Ran:** `cmd/aegis -controlplanes 3 -workers 0 -cp-memory 2048` (the user-facing talosctl command
  fixes control-plane count at 1, like docker/qemu, so this path drives the provider directly), then
  bootstrapped cp-1 and let cp-2/cp-3 join.
- **Saw:** 3 control-plane nodes (distinct IPs .22/.23/.24) all reached `Ready`; `talosctl etcd members`
  shows a **real 3-member quorum** (all non-learner, correct peer URLs). Clean teardown.
- **Verdict:** the provider's N-control-plane node loop + etcd quorum work. (Exposing a count flag on
  the talosctl command is a separate upstream choice; docker/qemu don't either.)

## 2026-06-13 — G5/stress: reboot & restart behaviour ✅ MEASURED (Claude-run, pending final acceptance)
- **Ran:** on a healthy 1cp+1worker cluster — (1) `talosctl reboot` the control plane; (2) `container
  stop` + `container start` the control plane (simulates a host/daemon restart of a node).
- **Saw:**
  - **(1) Talos refuses:** `method is not supported in container mode`. The node stays up, IP stable,
    configured. So the "Talos reboot/upgrade changes IP or wipes state" vector **cannot occur** —
    container-mode Talos nodes are immutable-ephemeral by Talos's own design; you recreate, not reboot.
  - **(2) container stop/start = node lost:** IP changed (`.2 → .4`, vmnet DHCP, no reservation) AND
    the node came back **blank** — `get machineconfig` fails TLS ("certificate signed by unknown
    authority") because `/system/state` + `/var` are tmpfs (RAM), so config + etcd data are wiped on
    cold restart. A single-control-plane cluster does not survive a node cold restart.
- **Surprised me:** Talos blocking reboot in container mode actually *narrows* the limitation — the
  only real trigger is a host/daemon restart, not anything Talos initiates.
- **Verdict / scope of the limitation:** within a session (create→use→destroy) and against Talos
  reboot/upgrade — **no impact**. On mac/daemon restart — the cluster is lost and must be recreated
  (~4 min). Two coupled causes: tmpfs (no persistence) + DHCP (no static IP). docker avoids both
  (persistent volumes + static IP); apple/container is a mild regression there, acceptable for
  ephemeral dev. A provider Reflect IP-refresh would NOT help (the restarted node is blank regardless);
  true cross-restart survival needs persistent `--volume` for /var+/system/state AND an upstream
  static-IP/DHCP-reservation in apple/container. Out of scope for the spike; documented as a known limit.
- **Update 2026-06-26:** the persistent-`--volume` half of that fix is now **implemented in code**
  (`feat/persistent-state-volumes`); see the G5 cross-restart entry below for the recipe and the
  sub-gates (G5a–G5d) that remain UNVERIFIED on hardware. The DHCP/IP half is still unsolved, so
  this entry's verdict — a single-control-plane cluster is lost on cold restart — still stands.

## 2026-06-13 — G5/usability: real workload + repo hardening ✅ (Claude-run, pending final acceptance)
- **Ran:** out-of-box `talosctl cluster create apple-container --name demo` (default image/k8s/2GiB,
  mirrors the official Talos flow) → deployed the canonical `nginx` deployment → exposed a Service →
  curled it from an in-cluster pod. (This is the runbook "Tutorial" walkthrough, run verbatim.)
- **Saw:** both nodes Ready; `nginx` 1/1 Running on the worker (pod IP `10.244.1.2` on the flannel
  pod network); Service `nginx` ClusterIP `10.96.11.115:80`; in-cluster
  `curl http://nginx.default.svc.cluster.local` → **HTTP 200**. CoreDNS + Service routing + kube-proxy
  + CNI all work. The `PodSecurity "restricted"` warning on bare nginx is expected (the cluster ships
  standard PSA admission). Clean teardown.
- **Repo hardening (public-repo protections):** branch protection on `main` — block force-push +
  deletion, require the 4 CI checks (build-test/lint/vuln/secrets, strict), `enforce_admins=false` so
  solo direct-push isn't blocked; secret scanning + push protection + Dependabot already enabled.
- **Verdict:** a genuinely usable Kubernetes cluster (real workload reachable in-cluster), not just
  nodes Ready. The runbook Tutorial is verified end to end — a forker can reproduce from zero.

## 2026-06-14 — FINAL ACCEPTANCE: Bin ran the runbook Tutorial end to end ✅ **Bin-accepted**
- **Who:** Bin, hands-on in his own terminal (NOT Claude-run) — the human-acceptance gate this whole
  effort was built around (the "one final total sign-off" from the Execution & acceptance model above).
- **Ran (verbatim from the runbook Tutorial, Path A, from a fresh checkout):** built `talosctl-apple`
  from a clean talos v1.13.3 clone + this repo's delta; `talosctl cluster create apple-container
  --name demo`; deployed the canonical nginx + Service; in-cluster curl; `cluster destroy`.
- **Saw:** `apple-container` subcommand present; 2 nodes health-green; nginx 1/1 Running on the worker
  (pod IP on the flannel net); in-cluster `curl http://nginx.default.svc.cluster.local` → **HTTP 200**;
  teardown left `container ls -a` empty.
- **Found + fixed during the run (the point of a real hands-on pass):** (1) the Path A `cp` block had
  a `$C`-prefix bug that only worked via a fallback → rewritten to clean relative-path copies; (2) a
  leftover kubeconfig context makes talosctl rename the new one (`admin@demo` → `admin@demo-1`) → noted
  in the Verify step. Both folded back into `runbook.md`.
- **Verdict:** **Bin-accepted 2026-06-14.** The provider, the upstream integration, and the forker
  runbook are verified by the human, from zero, on a fresh checkout. The spike's acceptance gate is cleared.

## 2026-06-14 — MetalLB L2 LoadBalancer ✅ PASS, incl. the host-facing ARP (Claude-run, pending final acceptance)
- **Why:** the downstream WS0 work (greeter on local Talos) needs MetalLB + ingress-nginx, and the
  sibling qemu-on-macOS path is known-broken there (#12834: vmnet-shared drops MetalLB L2's gratuitous
  ARP, so the VIP is unreachable). apple/container also uses vmnet — untested until now.
- **Ran:** cluster up → install MetalLB v0.14.8 → L2 IPAddressPool in the node subnet (10.5.0.240-250)
  → expose nginx as `type=LoadBalancer` → curl the assigned VIP from inside the cluster AND from the host.
- **Saw:** MetalLB assigned `EXTERNAL-IP 10.5.0.240`; **in-cluster curl → HTTP 200**; **host curl →
  HTTP 200**; and the host's ARP table learned the VIP (`10.5.0.240 at f2:6c:.. on bridge105`) — i.e.
  apple/container's vmnet **forwarded the gratuitous ARP** that the qemu-vmnet path drops. (The speaker
  DaemonSet `rollout status` timed out in the script, but the working host-ARP + HTTP 200 prove the
  speaker was announcing — a check artifact, not a failure.) Clean teardown.
- **Verdict:** MetalLB L2 LoadBalancer works end to end on apple/container, including host reachability
  — the exact thing the buggy qemu route fails. Closes the WS0 networking risk and is a concrete
  upstream differentiator ("a third macOS path with cleaner LoadBalancer networking than qemu").
  Remaining untested: ingress-nginx specifically (rides on the now-working LoadBalancer Service).

## 2026-06-14 — L7 ingress: ingress-nginx + Gateway API ✅ both PASS (Claude-run, pending final acceptance)
- **Why:** WS0 needs L7 ingress. **kubernetes/ingress-nginx is RETIRED** (archived 2026-03-24; no
  further releases/bugfixes/security; its README directs new users to Gateway API). So both the latest
  ingress-nginx AND the modern Gateway API were tested on apple/container (cluster k8s v1.36.1).
- **Ran:** MetalLB pool, then (A) ingress-nginx **v1.15.1** + Ingress; (B) Gateway API CRDs v1.5.1 +
  **Envoy Gateway v1.8.1** + Gateway + HTTPRoute. Host-header curls from the host.
- **Saw:**
  - **A:** ingress-nginx v1.15.1 → host `nginx.local` **HTTP 200**. (My earlier failures were the OLD
    v1.11.3, which predates k8s 1.36, plus install timing — not the substrate.)
  - **B:** Envoy Gateway came up; the Gateway got VIP `10.5.0.240`; `gw.local` → **HTTP 200**,
    `nope.local` → **404** (real host routing). (The first B attempt failed only because I installed
    standard Gateway API CRDs AND Envoy Gateway's bundled CRDs → version conflict; install.yaml-only fixed it.)
- **Verdict:** apple/container runs full L7 ingress end-to-end, host-reachable, via BOTH the legacy
  ingress-nginx and the modern Gateway API. The WS0 ingress question is closed; a strong upstream story.
  **Finding for WS0:** its plan still says "ingress-nginx" — that's retired; use Gateway API (verified here).

## 2026-06-26 — G5: cross-restart survival — host-bind FAILED on hardware, switched to NAMED VOLUMES ⚠️
- **VERIFIED FAIL — host-path bind-mount breaks Talos boot (run on real macOS 26 + Apple Silicon).**
  The first cut bind-mounted host dirs for the two state-bearing paths
  (`--volume <hostpath>:/var`, `--volume <hostpath>:/system/state`). On hardware the control-plane node
  **never reached maintenance mode.** Apple's `--volume <hostpath>:<container>` is a **virtio-fs host
  share**: the guest has no real ownership, so a `chmod` from inside the guest returns "operation not
  permitted". Talos's `block.MountController` **unconditionally** chmods `/system/state`, so it loops
  forever on:
  ```
  failed to chmod "/system/state": chmod /system/state: operation not permitted
  ```
  Consequence chain: the mount controller never settles → the maintenance API (`:50000`) **never opens**
  → `apply-config` fails with **connection refused**. The host-bind approach is a dead end for any Talos
  path that chmods its mounts — which `/system/state` always does.
- **VERIFIED — Apple NAMED VOLUMES are block-backed, guest-owned, and chmod-able.** `container volume
  create <name>` produces a block-backed ext4 volume owned by the guest root, so the in-guest chmod
  SUCCEEDS. Isolated busybox A/B test (same image, two mounts) made the difference unambiguous:
  ```sh
  container volume create chmodtest
  # named volume -> chmod OK
  container run --rm --volume chmodtest:/mnt busybox sh -c 'chmod 0700 /mnt && echo NAMED_OK'
  #   -> NAMED_OK
  # host-path bind -> chmod FAILS
  container run --rm --volume "$PWD/hostbind":/mnt busybox sh -c 'chmod 0700 /mnt || echo HOST_FAIL'
  #   -> chmod: /mnt: Operation not permitted
  #   -> HOST_FAIL
  ```
  So the fix is structural, not a flag tweak: state-bearing mounts MUST be named volumes.
- **What changed (code).** The provider no longer tmpfs-mounts and no longer host-binds the two
  state-bearing paths. `/var` (etcd data) and `/system/state` (machine config + PKI) are now per-cluster,
  per-node **named volumes** — `--volume <cluster>-<node>-var:/var` and
  `--volume <cluster>-<node>-system-state:/system/state` (names sanitized: lowercase, invalid chars →
  `-`; single source of truth in `nodeVolumeNames`). `Create` creates each volume and refuses to boot if
  one already EXISTS (stale state — tells the operator to `destroy` first); `Destroy` deletes them
  (idempotent: "not found" treated as success). Recipe + tests are green (`go build/vet/test`, `gofmt`);
  the runtime claims below are NOT yet observed.
- **Why this is necessary-but-NOT-sufficient (do not read this as "restart works").** The cold-restart
  loss had **two coupled causes** — tmpfs (state in RAM, wiped) AND vmnet DHCP (IP moves, no
  reservation). Named volumes remove the first. A restarted control-plane now keeps its config + etcd
  data, but its IP still changes, so the apiserver/etcd serving certs (SANs pinned to the old IP) go
  stale. **Restart survival is still expected to FAIL** on the cert mismatch (G5c).
- **STILL UNVERIFIED sub-gates — each needs hands-on testing on real macOS 26 + Apple Silicon:**

  - **G5a — does Talos actually boot to maintenance AND does etcd start on a NAMED-VOLUME-backed `/var`?**
    The chmod wall is cleared (named volume is guest-owned), so the node should now reach maintenance;
    the remaining open question is whether etcd comes up clean on the ext4 volume.
    ```sh
    aegis -name g5 -workers 0 && export TALOSCONFIG=_out/clusters/g5/talosconfig
    container logs g5-controlplane-1 2>&1 | grep -iE 'chmod|operation not permitted'  # EXPECT: empty now
    talosctl bootstrap && talosctl health
    talosctl -n <cp-ip> logs etcd 2>&1 | grep -iE 'permission|chown|uid|gid|data-dir' # EXPECT: clean
    ```
  - **G5c — persistent state + IP change on cold restart: EXPECTED to still fail on cert SAN mismatch.**
    State now survives the restart; the moved IP does not. Documents the remaining coupled cause.
    ```sh
    container stop g5-controlplane-1 && container start g5-controlplane-1
    NEW=$(container inspect g5-controlplane-1 | jq -r '.[0].status.networks[0].ipv4Address' | cut -d/ -f1)
    talosctl -n "$NEW" get machineconfig   # EXPECT: returns config now (state persisted on the volume) — the win
    talosctl -n "$NEW" health              # EXPECT: still FAILS — x509 cert SAN does not cover $NEW
    ```
- **FIXED — `aegis -destroy` now cleans a FAILED create (label sweep).** Previously Destroy reflected
  recorded state only (`Reflect` / `state.yaml`), so it could tear down only a cluster whose Create
  reached `saveState`; a Create that FAILED before that (observed: a stuck container, and now named
  volumes, left behind) had no `state.yaml` and the leftovers had to be removed by hand. Now:
  - **Create stamps the cluster labels on volumes too.** Containers already carried
    `--label talos.owned=true` + `talos.cluster.name=<name>` (buildRunArgs); `volumeCreate` now stamps the
    same `talos.cluster.name`/`talos.owned` labels on each `/var` and `/system/state` named volume.
  - **Destroy runs a label sweep in addition to the recorded-node pass.** Keyed on
    `talos.cluster.name=<name>`, it lists every matching container (stop + rm) and volume (delete), all
    idempotent. The sweep runs even when the recorded node list is empty/missing, so it reclaims
    orphaned containers AND volumes from a half-created cluster. The `container` CLI has **no native
    `--label`/`--filter`** on `list` or `volume list` (verified from `--help`), so the sweep lists
    `--format json` and matches labels client-side (`listContainersByLabel` / `listVolumesByLabel`).
  - **`-destroy` tolerates a missing `state.yaml`.** `cmd/aegis` no longer aborts when `Reflect` fails
    with `fs.ErrNotExist`; it falls back to a label-based Destroy keyed on `-name` + `-state-dir`
    (`apple.ClusterRef`). So `aegis -name X -destroy` cleans a half-created cluster X. Other `Reflect`
    errors (corrupt/unparseable state) still surface. Unit-locked in `provider_test.go`
    (selector string, volume-label/selector symmetry, the client-side JSON filter, and the missing-state
    ref). Live container behavior remains to be confirmed on hardware in the next hands-on session.
- **Verdict:** host-bind approach VERIFIED-FAILED and abandoned; named-volume recipe IMPLEMENTED and
  unit-locked; the chmod wall is proven cleared by the busybox A/B test, but the full Talos boot
  (G5a) and cross-restart behavior (G5c) remain UNVERIFIED. This is a SPIKE repo — runtime behavior
  beyond the busybox primitive cannot be confirmed here. The next hands-on session runs G5a/G5c on real
  hardware and records the observations as their own first-person entries.

---

Fill each first-person as the gate runs. Surprises and dead-ends are the most valuable
entries — they are what a reviewer reads as a human having actually done the work.
