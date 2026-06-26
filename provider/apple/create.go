// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package apple

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strconv"

	"github.com/siderolabs/talos/pkg/machinery/constants"
	"github.com/siderolabs/talos/pkg/provision"
)

// Create provisions a Talos cluster on Apple's `container` runtime.
//
// Flow mirrors the in-tree docker provider's shape (init state -> network -> launch
// control-plane then workers -> record ClusterInfo -> save), with one forced divergence:
// the DHCP reconciliation (see reconcileConfigs). `container run` pulls the image on demand,
// so there is no explicit image-pull step.
func (p *provisioner) Create(ctx context.Context, request provision.ClusterRequest, opts ...provision.Option) (provision.Cluster, error) {
	if err := validateClusterRequest(request); err != nil {
		return nil, err
	}

	options := provision.DefaultOptions()

	for _, opt := range opts {
		if err := opt(&options); err != nil {
			return nil, err
		}
	}

	statePath := clusterStatePath(request)

	fmt.Fprintf(options.LogWriter, "creating state directory in %q\n", statePath)

	state, err := provision.NewState(statePath, ProviderName, request.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize provisioner state: %w", err)
	}

	fmt.Fprintln(options.LogWriter, "ensuring network", request.Network.Name)

	if err = p.ensureNetwork(ctx, request.Network); err != nil {
		return nil, fmt.Errorf("failed to ensure network: %w", err)
	}

	// Launch order: control-plane first (so the first node is the control plane whose IP becomes
	// the cluster endpoint), then workers.
	orderedReqs := slices.Concat(request.Nodes.ControlPlaneNodes(), request.Nodes.WorkerNodes())

	// Persistent-state volumes: create each node's /var and /system/state named volumes before launch,
	// and refuse to boot onto stale state from a prior run (see prepareNodeVolumes). Volumes are stamped
	// with the cluster labels so the destroy label sweep can reclaim them even if this Create fails
	// before saveState (the half-created-cluster gap, docs/VERIFICATION.md G5).
	createVolume := func(ctx context.Context, name string) error {
		return p.volumeCreate(ctx, name, volumeLabels(request.Name)...)
	}

	if err = prepareNodeVolumes(ctx, request.Name, orderedReqs, p.volumeExists, createVolume); err != nil {
		return nil, err
	}

	fmt.Fprintln(options.LogWriter, "launching nodes (bare; maintenance mode)")

	nodes, err := p.createNodes(ctx, request, orderedReqs)
	if err != nil {
		return nil, err
	}

	// Everyday-correctness guard: every node must get a distinct vmnet IP. A regression that
	// handed nodes the same address (e.g. inspecting a shared name) would silently break the
	// cluster, so we fail loudly instead.
	if err = assertDistinctIPs(nodes); err != nil {
		return nil, err
	}

	// DHCP reconciliation: nodes booted bare; patch each config's control-plane endpoint with the
	// discovered control-plane IP and apply it over the maintenance API.
	if err = p.reconcileConfigs(ctx, request, orderedReqs, nodes, &options); err != nil {
		return nil, err
	}

	controlPlaneIP := nodes[0].IPs[0]
	kubernetesEndpoint := "https://" + net.JoinHostPort(controlPlaneIP.String(), strconv.Itoa(constants.DefaultControlPlanePort))

	state.ClusterInfo = provision.ClusterInfo{
		ClusterName: request.Name,
		Network: provision.NetworkInfo{
			Name:         request.Network.Name,
			CIDRs:        request.Network.CIDRs,
			GatewayAddrs: request.Network.GatewayAddrs,
			MTU:          request.Network.MTU,
		},
		Nodes:              nodes,
		KubernetesEndpoint: kubernetesEndpoint,
	}

	if err = state.Save(); err != nil {
		return nil, err
	}

	return &result{
		clusterInfo: state.ClusterInfo,
		statePath:   statePath,
	}, nil
}

// validateClusterRequest rejects requests that would break provisioning, rather than failing
// deep inside Create (e.g. a request with no control-plane node would otherwise panic when we
// take the first node's IP as the cluster endpoint). A worker-only count is the meaningful
// boundary: >= 1 control-plane is required; 0 workers (a single control-plane cluster) is valid.
func validateClusterRequest(request provision.ClusterRequest) error {
	if len(request.Nodes.ControlPlaneNodes()) == 0 {
		return fmt.Errorf("cluster %q: at least one control-plane node is required, got %d nodes (%d control-plane)",
			request.Name, len(request.Nodes), len(request.Nodes.ControlPlaneNodes()))
	}

	return nil
}

// prepareNodeVolumes creates each node's persistent /var and /system/state named volumes, and guards
// against booting onto stale state.
//
// The guard is the load-bearing side effect of moving from tmpfs to persistent volumes: a /var volume
// left behind by a prior run carries old etcd data, and a stale /system/state carries an old machine
// config + PKI. Reusing either silently would boot a node into a stale, half-broken cluster (wrong
// certs, divergent etcd) rather than a clean one. So for each volume we refuse if it already EXISTS
// (telling the operator to destroy this cluster first) and otherwise create it fresh — never silently
// reuse a stale volume, never silently wipe one.
//
// exists/create are injected (p.volumeExists / p.volumeCreate in production) so the guard is unit-testable
// without the `container` CLI.
func prepareNodeVolumes(
	ctx context.Context,
	clusterName string,
	reqs []provision.NodeRequest,
	exists func(context.Context, string) (bool, error),
	create func(context.Context, string) error,
) error {
	for _, req := range reqs {
		varVol, systemStateVol := nodeVolumeNames(clusterName, req.Name)

		for _, vol := range []string{varVol, systemStateVol} {
			present, err := exists(ctx, vol)
			if err != nil {
				return fmt.Errorf("checking volume %q for node %q: %w", vol, req.Name, err)
			}

			if present {
				return fmt.Errorf(
					"node %q: named volume %q already exists (stale state from a prior run); "+
						"run destroy for this cluster first — refusing to reuse it",
					req.Name, vol,
				)
			}

			if err := create(ctx, vol); err != nil {
				return fmt.Errorf("creating volume %q for node %q: %w", vol, req.Name, err)
			}
		}
	}

	return nil
}

// assertDistinctIPs fails if any two nodes share an IP (an everyday-correctness regression guard).
func assertDistinctIPs(nodes []provision.NodeInfo) error {
	seen := make(map[string]string, len(nodes))

	for _, node := range nodes {
		if len(node.IPs) == 0 {
			return fmt.Errorf("node %q has no IP", node.Name)
		}

		ip := node.IPs[0].String()
		if other, dup := seen[ip]; dup {
			return fmt.Errorf("nodes %q and %q were both assigned IP %s", other, node.Name, ip)
		}

		seen[ip] = node.Name
	}

	return nil
}
