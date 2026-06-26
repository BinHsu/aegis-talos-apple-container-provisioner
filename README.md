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
  - **Restart survival (hardware-verified 2026-06-26):** named volumes for `/var` (etcd) and `/system/state` (PKI + machineconfig) are hardware-verified (G5a–G5c); etcd data survives cold restart. When `-dns-domain` is set, the `kubectl`/kubeconfig path fully recovers with zero reconfiguration — the FQDN endpoint stays valid as the DNS forwarder tracks the new DHCP IP (G6b). The `talosctl -n` side still requires the current IP after restart (`talosctl`'s `--nodes` flag does not accept hostnames). See G5 and G6b in [`docs/VERIFICATION.md`](docs/VERIFICATION.md).
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

## Stable hostname endpoint (v0.2.0)

By default, `aegis` names every container as `<cluster>-<role>-N.<domain>` (e.g.
`aegis-controlplane-1.aegis`) and sets `cluster.controlPlane.endpoint` and the certificate SANs
to that FQDN. After a cold restart the DHCP IP changes but the FQDN stays resolvable — so
`kubectl` and `talosctl` keep working without re-pointing, as long as Talos can reach the node by
its new IP (which it can, because the serving cert includes the FQDN in its SANs).

**One-time setup (must re-run after every macOS reboot):**

```sh
sudo container system dns create aegis
```

This installs `/etc/resolver/containerization.aegis`, forwarding `*.aegis` to
`127.0.0.1:2053` (the container DNS forwarder). The forwarder automatically tracks IP changes
across container restarts — no manual update needed when the DHCP IP shifts.

**CLI flag:**

| Flag | Default | Description |
|------|---------|-------------|
| `-dns-domain` | `aegis` | DNS domain for FQDN container naming. Set to `""` to disable FQDN naming and fall back to IP-only (v0.1.x behaviour). |

**To disable FQDN naming** and fall back to IP-based endpoint (v0.1.x):

```sh
aegis -dns-domain ""
```

**Verification status (2026-06-26):** host-to-container DNS and automatic IP-update after restart
(busybox, G6a) and full Talos cold-restart endpoint survival (G6b) are both **hardware-verified**.
Cold-restart resilience applies to the `kubectl`/kubeconfig path: the node returns Ready and etcd
data persists without any reconfiguration. The `talosctl -n` path still requires the current IP
after restart — `talosctl`'s `--nodes` flag does not accept hostnames; pass `-n <current-IP>` for
node targeting. See G6 in [`docs/VERIFICATION.md`](docs/VERIFICATION.md).

## Requirements

- macOS 26+, Apple Silicon
- [`container`](https://github.com/apple/container) >= 1.0.0
- `talosctl`

## License

[MIT](LICENSE)
