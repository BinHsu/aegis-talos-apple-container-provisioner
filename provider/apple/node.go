// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package apple

import (
	"context"
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"
	"time"

	"github.com/siderolabs/talos/pkg/machinery/constants"
	"github.com/siderolabs/talos/pkg/provision"
)

// nodeTmpfsPaths returns the in-VM paths that must be independent, writable mounts for a
// Talos node to boot in container mode — and that are genuinely ephemeral (Talos repopulates
// them every boot, so RAM-backed tmpfs is correct).
//
// Derivation from the docker provider's mount set, with the apple/container deltas the
// G1-G4 spike established (see docs/runbook.md G2/G4):
//
//   - docker tmpfs-es /run,/system,/tmp and mounts /var, /system/state, and constants.Overlays
//     as named *volumes*. apple/container has no docker-style named volumes, so the ephemeral
//     ones become a --tmpfs. That is fine: they are runtime dirs Talos repopulates, and making
//     them real mount points is exactly what Talos's setupSharedFilesystems (MS_SHARED|MS_REC on
//     /,/var,/etc/cni,/run) requires — without them the boot sequence fails with EINVAL (G2/G4).
//
//   - /opt is EXCLUDED. A fresh docker *volume* copies up the image's content, so docker's /opt
//     volume keeps the shipped /opt/cni/bin (loopback, flannel). --tmpfs does NOT copy up — it is
//     always empty — so tmpfs-ing /opt shadows the CNI plugins and pod sandbox creation fails
//     ("failed to find plugin flannel/loopback in /opt/cni/bin"), leaving coredns stuck (G4).
//     apple/container's rootfs is writable, so leaving /opt unmounted preserves the binaries and
//     still lets flannel's install-cni write into /opt/cni/bin.
//
//   - /var (constants.EphemeralMountPoint) and /system/state (constants.StateMountPoint) are NO
//     LONGER tmpfs. They are the only two mounts carrying state that must survive a container cold
//     restart (machine config + PKI live in /system/state; etcd data lives in /var), so they are
//     now persistent Apple `container` NAMED VOLUMES via nodeVolumeNames / buildRunArgs (--volume).
//     Keeping them on RAM-backed tmpfs is what wiped a single-control-plane cluster on a daemon
//     restart (the G5 cross-restart gap). NB: this is necessary but not sufficient for restart
//     survival — the vmnet DHCP IP still moves on restart, so API-server/etcd cert SANs go stale
//     (see G5c).
//
//     Why NAMED VOLUMES and not host-path bind-mounts (verified on hardware): Apple's
//     `--volume <hostpath>:<container>` is a virtio-fs host share. The guest has no real ownership
//     of that share, so a `chmod` from inside the guest returns "operation not permitted". Talos's
//     block.MountController unconditionally chmods /system/state, so a host-bind /system/state loops
//     forever ("failed to chmod \"/system/state\": chmod /system/state: operation not permitted"),
//     the maintenance API (:50000) never opens, and apply-config fails with connection refused.
//     Apple named volumes (`container volume create`) are block-backed ext4 owned by the guest root,
//     so the guest chmod SUCCEEDS. See docs/VERIFICATION.md G5.
func nodeTmpfsPaths() []string {
	paths := []string{"/run", "/system", "/tmp"}

	for _, overlay := range constants.Overlays {
		if overlay.Path == "/opt" {
			continue
		}

		paths = append(paths, overlay.Path)
	}

	return paths
}

// clusterStatePath is the per-cluster state directory the provisioner persists into:
// <StateDirectory>/<clusterName>. It is the same value provision.ReadState reconstructs and that
// State.StatePath() returns, so create and destroy (including a fresh-process Destroy via Reflect)
// agree on one base without any extra persisted field. provision.NewState writes state.yaml here.
func clusterStatePath(clusterReq provision.ClusterRequest) string {
	return filepath.Join(clusterReq.StateDirectory, clusterReq.Name)
}

// nodeVolumeNames returns the two Apple `container` NAMED VOLUME names backing a node's state-bearing
// in-VM paths: /var (etcd data) and /system/state (machine config + PKI). Named volumes are
// block-backed ext4 owned by the guest root, so Talos's block.MountController can chmod them — the
// host-path bind-mount that virtio-fs rejects could not (see node.go header and docs/VERIFICATION.md G5).
//
// Scheme: <cluster>-<node>-var and <cluster>-<node>-system-state, sanitized to a valid volume name
// (lowercase; any char outside [a-z0-9-] replaced with '-'). The suffix is "system-state" because a
// volume name cannot contain the in-VM "/system/state".
//
// This is the single source of truth: buildRunArgs (mount), Create (existence guard + create), and
// Destroy (delete) all derive names here, so the destroy path can never target a different volume
// than the one create provisioned.
func nodeVolumeNames(clusterName, nodeName string) (varVol, systemStateVol string) {
	base := clusterName + "-" + nodeName

	return sanitizeVolumeName(base + "-var"), sanitizeVolumeName(base + "-system-state")
}

// sanitizeVolumeName lowercases s and replaces every character outside [a-z0-9-] with '-', yielding a
// stable, valid Apple `container` volume name from an arbitrary cluster/node identifier.
func sanitizeVolumeName(s string) string {
	var b strings.Builder

	b.Grow(len(s))

	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}

	return b.String()
}

// buildRunArgs assembles the `container run` argument vector for one node from the verified
// G4 recipe. It is a pure function so the recipe can be unit-tested (incl. BVA on node fields)
// without launching a VM.
func buildRunArgs(clusterReq provision.ClusterRequest, nodeReq provision.NodeRequest) []string {
	args := []string{
		"run", "--detach",
		"--name", nodeReq.Name,
		// G2: machined dies on fsopen(tmpfs) EPERM without full capabilities. apple/container
		// has no --privileged; --cap-add ALL is the equivalent of docker's Privileged:true.
		"--cap-add", "ALL",
	}

	// Memory limit. Verified format is a unit-suffixed value ("4096MB"); a bare suffix like
	// "4096M" is rejected. Control-plane nodes need >= ~2GB or the 512Mi apiserver static pod
	// is OOM-killed silently at create (G4).
	if nodeReq.Memory > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dMB", nodeReq.Memory/(1024*1024)))
	}

	if nodeReq.NanoCPUs > 0 {
		args = append(args, "--cpus", fmt.Sprintf("%d", nodeReq.NanoCPUs/(1000*1000*1000)))
	}

	for _, path := range nodeTmpfsPaths() {
		args = append(args, "--tmpfs", path)
	}

	// Persistent state. /var (etcd) and /system/state (machine config + PKI) are backed by per-cluster,
	// per-node Apple `container` NAMED VOLUMES so cluster state survives a container cold restart — they
	// used to be tmpfs (RAM), which wiped the cluster on a daemon/host restart (G5). Named volumes (not
	// host-path bind-mounts) are mandatory: a host-bind /system/state is a virtio-fs share the guest
	// cannot chmod, so Talos's block.MountController loops on "chmod: operation not permitted" and the
	// node never reaches maintenance mode. The volumes are created (and stale-state-guarded) in Create
	// before launch; Destroy deletes them.
	varVol, systemStateVol := nodeVolumeNames(clusterReq.Name, nodeReq.Name)
	args = append(args,
		"--volume", varVol+":"+constants.EphemeralMountPoint,
		"--volume", systemStateVol+":"+constants.StateMountPoint,
	)

	// Labels mirror the docker provider (debugging + future Reflect); node IDs are also tracked
	// in state.yaml so teardown does not depend on label-listing.
	args = append(args,
		"--label", "talos.owned=true",
		"--label", "talos.cluster.name="+clusterReq.Name,
		"--label", "talos.type="+nodeReq.Type.String(),
	)

	// Environment. PLATFORM=container makes Talos take its container code path; TALOSSKU is
	// informational (matches the docker provider).
	//
	// NB: unlike docker we deliberately do NOT inject USERDATA here. The docker provider can bake
	// the machine config in at launch because it assigns each node a static IP, so the config's
	// cluster.controlPlane.endpoint (and apiserver cert SANs) are known up front. apple/container
	// assigns IPs via vmnet DHCP (no static --ip; verified G3), so the control-plane IP is not
	// known until after launch. Nodes therefore boot bare into maintenance mode; Create discovers
	// the IPs, patches the endpoint, and applies the config over the maintenance API (the
	// post-launch reconciliation that the G4 manual flow proved). This keeps the whole DHCP
	// divergence inside the provider — no change to the upstream pkg/provision framework.
	args = append(args,
		"--env", "PLATFORM=container",
		"--env", fmt.Sprintf("TALOSSKU=%dCPU-%dRAM", nodeReq.NanoCPUs/(1000*1000*1000), nodeReq.Memory/(1024*1024)),
	)

	if clusterReq.Network.Name != "" {
		args = append(args, "--network", clusterReq.Network.Name)
	}

	// Image is the trailing positional argument.
	args = append(args, clusterReq.Image)

	return args
}

// ipDiscoveryTimeout bounds how long we wait for vmnet DHCP to assign a node its address.
const ipDiscoveryTimeout = 30 * time.Second

// createNode launches one Talos node and returns its NodeInfo once it has an IP.
func (p *provisioner) createNode(ctx context.Context, clusterReq provision.ClusterRequest, nodeReq provision.NodeRequest) (provision.NodeInfo, error) {
	args := buildRunArgs(clusterReq, nodeReq)

	if _, err := p.run(ctx, args...); err != nil {
		return provision.NodeInfo{}, fmt.Errorf("launching node %q: %w", nodeReq.Name, err)
	}

	// apple/container uses --name as the container ID.
	id := nodeReq.Name

	// Poll for the DHCP-assigned address (no static --ip; G3).
	addr, err := p.waitForIPv4(ctx, id)
	if err != nil {
		return provision.NodeInfo{}, err
	}

	return provision.NodeInfo{
		ID:       id,
		Name:     nodeReq.Name,
		Type:     nodeReq.Type,
		NanoCPUs: nodeReq.NanoCPUs,
		Memory:   nodeReq.Memory,
		IPs:      []netip.Addr{addr},
	}, nil
}

// waitForIPv4 polls `container inspect` until the node has a vmnet IPv4 or the timeout elapses.
func (p *provisioner) waitForIPv4(ctx context.Context, id string) (netip.Addr, error) {
	deadline := time.Now().Add(ipDiscoveryTimeout)

	for {
		addr, err := p.inspectIPv4(ctx, id)
		if err == nil {
			return addr, nil
		}

		if time.Now().After(deadline) {
			return netip.Addr{}, fmt.Errorf("timed out waiting for %q to get an IPv4: %w", id, err)
		}

		select {
		case <-ctx.Done():
			return netip.Addr{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// createNodes launches a set of nodes sequentially, returning their NodeInfo.
func (p *provisioner) createNodes(ctx context.Context, clusterReq provision.ClusterRequest, nodeReqs []provision.NodeRequest) ([]provision.NodeInfo, error) {
	nodes := make([]provision.NodeInfo, 0, len(nodeReqs))

	for _, nodeReq := range nodeReqs {
		info, err := p.createNode(ctx, clusterReq, nodeReq)
		if err != nil {
			return nodes, err
		}

		nodes = append(nodes, info)
	}

	return nodes, nil
}
