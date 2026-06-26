// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package apple

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/siderolabs/talos/pkg/provision"
)

// Destroy tears down a provisioned cluster. It is idempotent (stop/remove, volume deletes, and the
// state-dir removal all ignore "not found"/not-exist) and, per the G4 acceptance criterion, leaves
// `container ls -a` clean.
//
// Teardown runs in two passes, both idempotent:
//
//  1. Recorded-state pass: node IDs/names come from the cluster's recorded state (Info().Nodes), and
//     each node's /var and /system/state NAMED VOLUMES are deleted by their derived names. Deleting
//     these volumes is mandatory — skipping it would leave old etcd data + machine config + PKI
//     behind, so recreating a same-named cluster would hit the create-time stale-state guard (or boot
//     onto stale state). This pass is a no-op when Info().Nodes is empty.
//
//  2. Label sweep: containers and volumes are also listed by the cluster label
//     (talos.cluster.name=<name>) and stopped/removed/deleted. The CLI has no native label filter, so
//     the sweep lists `--format json` and matches client-side (see listContainersByLabel /
//     listVolumesByLabel). This pass closes the half-created-cluster gap: a Create that FAILED before
//     saveState leaves orphaned containers/volumes but no recorded node list, and the sweep reclaims
//     them from the labels alone. It runs even when Info().Nodes is empty/missing.
//
// Finally RemoveAll(statePath) sweeps the provisioner's own state dir (state.yaml); the named volumes
// are container-managed (not under statePath), so they are deleted by the two passes above, not here.
func (p *provisioner) Destroy(ctx context.Context, cluster provision.Cluster, opts ...provision.Option) error {
	options := provision.DefaultOptions()

	for _, opt := range opts {
		if err := opt(&options); err != nil {
			return err
		}
	}

	info := cluster.Info()

	// statePath is the provisioner's own state dir (state.yaml). It may be unset on a hand-built
	// Cluster; if so we skip the state-dir sweep but still stop/remove containers and delete volumes.
	statePath, statePathErr := cluster.StatePath()

	var errs []error

	errs = append(errs, p.destroyRecordedNodes(ctx, info, options.LogWriter)...)

	// Label sweep: reclaim any container/volume tagged for this cluster that the recorded-node pass
	// above missed — the load-bearing case is a Create that failed before saveState, which leaves
	// orphans but no node list. It runs even when Info().Nodes is empty/missing.
	errs = append(errs, p.sweepByLabel(ctx, info.ClusterName, options.LogWriter)...)

	if err := p.destroyNetwork(ctx, info.Network.Name); err != nil {
		errs = append(errs, err)
	}

	// Per-cluster sweep: removes the provisioner state dir (state.yaml). Volumes are container-managed,
	// not under statePath, so they are deleted per node above — not by this RemoveAll.
	if statePathErr == nil {
		if err := os.RemoveAll(statePath); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// destroyRecordedNodes is the recorded-state teardown pass: for each node in Info().Nodes it stops and
// removes the container, then deletes the node's /var and /system/state named volumes (names derived
// from the same nodeVolumeNames Create used). Every step ignores "not found", so it is idempotent and a
// no-op when Info().Nodes is empty. It collects errors rather than aborting, so one stuck node does not
// block teardown of the rest.
func (p *provisioner) destroyRecordedNodes(ctx context.Context, info provision.ClusterInfo, log io.Writer) []error {
	var errs []error

	for _, node := range info.Nodes {
		fmt.Fprintln(log, "destroying node", node.Name)

		if err := p.stop(ctx, node.ID); err != nil {
			errs = append(errs, err)
		}

		if err := p.remove(ctx, node.ID); err != nil {
			errs = append(errs, err)
		}

		varVol, systemStateVol := nodeVolumeNames(info.ClusterName, node.Name)

		for _, vol := range []string{varVol, systemStateVol} {
			if err := p.volumeDelete(ctx, vol); err != nil {
				errs = append(errs, err)
			}
		}
	}

	return errs
}

// sweepByLabel reclaims every container and named volume tagged talos.cluster.name=<clusterName>,
// independent of any recorded node list. It is the half-created-cluster fix: a Create that failed
// before saveState leaves orphaned containers/volumes but no state.yaml, and this sweep finds them from
// their labels alone. Each step is idempotent (stop/remove/volumeDelete ignore "not found"). An empty
// clusterName is skipped — the selector "talos.cluster.name=" would match nothing useful.
func (p *provisioner) sweepByLabel(ctx context.Context, clusterName string, log io.Writer) []error {
	if clusterName == "" {
		return nil
	}

	selector := clusterLabelSelector(clusterName)

	var errs []error

	if names, err := p.listContainersByLabel(ctx, selector); err != nil {
		errs = append(errs, err)
	} else {
		for _, name := range names {
			fmt.Fprintln(log, "sweeping container", name)

			if err := p.stop(ctx, name); err != nil {
				errs = append(errs, err)
			}

			if err := p.remove(ctx, name); err != nil {
				errs = append(errs, err)
			}
		}
	}

	if vols, err := p.listVolumesByLabel(ctx, selector); err != nil {
		errs = append(errs, err)
	} else {
		for _, vol := range vols {
			fmt.Fprintln(log, "sweeping volume", vol)

			if err := p.volumeDelete(ctx, vol); err != nil {
				errs = append(errs, err)
			}
		}
	}

	return errs
}
