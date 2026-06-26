# aegis-talos-apple-container-provisioner

A `talosctl` provisioner that runs local Talos Linux clusters on Apple's [`container`](https://github.com/apple/container) runtime (macOS 26+, Apple Silicon) — one micro-VM per node, no Docker daemon anywhere in the stack.

> **Status: a proven spike, not maintained tooling.** It answers one question — *can `talosctl cluster create` run a real Talos cluster on macOS with no Docker API?* — and the answer is yes, verified end to end (see [`docs/runbook.md`](docs/runbook.md) and [`docs/VERIFICATION.md`](docs/VERIFICATION.md)). It was pitched upstream and **declined on principled grounds** — see [siderolabs/talos#13587](https://github.com/siderolabs/talos/discussions/13587). It stays here as a standalone dev/CI substrate. The conclusions live in a blog post; the code is the receipts. Do not build production on it.

## Why

Production Kubernetes shed the Docker daemon years ago — `dockershim` is gone, the runtime underneath is containerd or CRI-O, and Talos itself ships no Docker. But the local dev loop never followed: `kind`, `minikube`, and Talos's own `docker` provisioner all still ride a Docker daemon behind Docker Desktop or OrbStack.

Apple's `container` (1.0.0, June 2026) runs OCI images as per-node micro-VMs and speaks no Docker API at all. The Docker dependency in local Talos is thinner than it looks:

- The Talos node artifact is a standard OCI image (`ghcr.io/siderolabs/talos`).
- The management plane (`talosctl gen config` / `apply-config` / `bootstrap`) talks the Talos gRPC API on port 50000 — no Docker anywhere.
- Only the local provisioner — create a network, start node containers — touches a container runtime.

So a provisioner that execs the `container` CLI — the same pattern the in-tree QEMU provisioner uses with `qemu` — gives a local Talos cluster with no Docker daemon in the stack, while keeping nodes lightweight (container mode) rather than full VMs.

That is the niche: **no Docker *and* lightweight.** The `docker` provisioner is lightweight but needs a Docker daemon; the `qemu` provisioner needs no Docker but boots full VMs. apple/container fills the remaining quadrant.

## What it is (and isn't)

- **Is:** a no-Docker, per-node-kernel, Apple-Silicon-native substrate for *ephemeral local dev and CI* — the same scope Talos officially assigns its `docker` provisioner ("CI pipelines and local testing… not suitable for production deployments").
- **Isn't:** a production substrate or an upstream path. Talos runs here in *container mode*, so disk-install, in-place upgrade, and reboot don't apply, and a cluster does not yet survive a host cold restart (recreate ≈ 4 min). For full-lifecycle local Talos, use the supported `qemu` provisioner.
  - **Restart survival, in progress (honest status):** node `/var` (etcd) and `/system/state` (machine config + PKI) are now **persistent host bind-mounts**, not RAM-backed tmpfs, so that state is no longer wiped on a cold restart. This is **necessary but not sufficient** — the vmnet DHCP IP still changes on restart, so the apiserver/etcd serving-cert SANs go stale and a single-control-plane cluster still does not come back healthy. Restart survival also needs an upstream static-IP / DHCP-reservation in `container`. The persistent-volume recipe is implemented in code; its on-hardware behavior (etcd on virtio-fs, mount propagation, fsync) is **unverified** — see G5a–G5d in [`docs/VERIFICATION.md`](docs/VERIFICATION.md). Do not rely on restart working.
- **One concrete edge over `docker` on Mac:** Talos's docs note VIPs aren't supported under docker on macOS; here a MetalLB L2 LoadBalancer VIP is **host-reachable** — the provider's vmnet path forwards the gratuitous ARP that the qemu path drops ([#12834](https://github.com/siderolabs/talos/issues/12834)). L7 ingress works via Gateway API / Envoy Gateway.

## Design constraints

- **Go**, implementing the provisioner interface from `siderolabs/talos/pkg/provision` — the integration is a directory move, not a rewrite (verified: builds against the real interface).
- Exec the `container` CLI; no Swift, no private APIs.
- No new configuration surface beyond what the docker/qemu provisioners already expose.

## What the spike validated

| Question | Finding |
|---|---|
| Kernel feature set | apple/container's Kata-derived kernel (6.18.15) carries the kubelet/CNI features built-in; CNI and coredns work once `/opt` is kept off tmpfs (tmpfs would shadow the shipped `/opt/cni/bin`). |
| Init model | `machined` tolerates running under Apple's `vminitd` (PID 1) given `--cap-add ALL` — the `Privileged: true` equivalent the docker provisioner sets. |
| Networking | Per-node DHCP IPs (no static-IP option), node-to-node reachable. The provider reconciles the DHCP address *after* boot. MetalLB VIP host-reachable; L7 via Gateway API / Envoy Gateway. |

The DHCP reconciliation — launch the node bare, read its assigned IP with `container inspect`, patch `cluster.controlPlane.endpoint`, then apply the config over the maintenance API — is the design crux, and is why a native provider works where a Docker-API shim cannot. See [`docs/ADR/0001-native-provider-vs-docker-shim.md`](docs/ADR/0001-native-provider-vs-docker-shim.md).

## Requirements

- macOS 26+, Apple Silicon
- [`container`](https://github.com/apple/container) >= 1.0.0
- `talosctl`

## License

[MIT](LICENSE)
