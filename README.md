# aegis-talos-apple-container-provisioner

Spike toward an upstream `talosctl` provisioner that runs local Talos Linux clusters on Apple's [`container`](https://github.com/apple/container) runtime (macOS 26+, Apple Silicon, one micro-VM per container).

> **Status: exploration, not maintained tooling.** This repo exists to answer one question — *can `talosctl cluster create` work on macOS without a Docker API?* — in the shape an upstream contribution would take (following the macOS qemu provider precedent, [siderolabs/talos#10537](https://github.com/siderolabs/talos/issues/10537), shipped 2025). Conclusions land in a blog post; the code here is the receipts. If the experiment proves out and upstream signals interest, this moves into `siderolabs/talos` as a `pkg/provision` provider and this repo gets archived. Do not build on it.

## Why

Talos's documented local-dev quickstart on macOS routes through the Docker Engine API (`talosctl cluster create --provisioner docker`). A qemu provisioner also landed for macOS in 2025 ([PR #11110](https://github.com/siderolabs/talos/pull/11110)) but carries open networking bugs ([#12727](https://github.com/siderolabs/talos/issues/12727), [#12834](https://github.com/siderolabs/talos/issues/12834)). The Docker dependency is still thinner than it looks:

- The Talos node artifact is a standard OCI image (`ghcr.io/siderolabs/talos`).
- The management plane (`talosctl gen config` / `apply-config` / `bootstrap`) talks the Talos gRPC API on port 50000 — no Docker anywhere.
- Only the local provisioner — create a network, start node containers — touches the Docker API.

Apple's `container` (1.0.0, June 2026) runs OCI images as micro-VMs with a per-container kernel, but exposes no Docker API. The hypothesis: a provisioner that execs the `container` CLI — the same pattern the in-tree QEMU provisioner uses with `qemu` — closes the gap, and replaces Docker mode's shared-kernel containers with VM-isolated nodes.

## Design constraints (upstream-shaped from day one)

- **Go**, implementing the provisioner interface from `siderolabs/talos/pkg/provision` — upstreaming should be a directory move, not a rewrite.
- Exec the `container` CLI; no Swift, no private APIs.
- No new configuration surface beyond what the docker/qemu provisioners already expose.

## Risks to validate before writing any provisioner code

| Risk | Question to answer |
|---|---|
| Kernel feature set | Does apple/container's Kata-derived default kernel carry what kubelet/CNI need (overlayfs, br_netfilter, nf_conntrack, …)? Containerization supports custom kernels if not. |
| Init model | Talos `machined` expects to be PID 1; in an apple/container VM, PID 1 is `vminitd`. Does `machined` tolerate running as a child process? |
| Networking | Stable per-node IPs and node-to-node reachability on the vmnet-backed container network (macOS 26+). |

## Requirements

- macOS 26+, Apple Silicon
- [`container`](https://github.com/apple/container) >= 1.0.0
- `talosctl`

## License

[MIT](LICENSE)
