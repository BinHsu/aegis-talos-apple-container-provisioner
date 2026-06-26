// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package apple

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/siderolabs/talos/pkg/provision"
)

// Destroy tears down a provisioned cluster. It is idempotent (stop/remove and the volume-dir
// removals ignore "not found"/not-exist) and, per the G4 acceptance criterion, leaves
// `container ls -a` clean.
//
// Node IDs and names come from the cluster's recorded state (cluster.StatePath() + Info().Nodes),
// so teardown does not depend on `container ls` label filtering (which the CLI does not support),
// and it works in a fresh process via Reflect/ReadState.
//
// Critical side effect of persistent state volumes: removing the per-node /var and /system/state
// host bind-mount dirs is mandatory. Skipping it would leave old etcd data + machine config + PKI
// on disk, so recreating a same-named cluster would boot onto stale state instead of clean nodes.
// The volume dirs live under statePath (<statePath>/<nodeName>/{var,system-state}), so the final
// RemoveAll(statePath) is the per-cluster parent sweep; the explicit per-node removals below make
// the intent unmistakable and keep cleanup correct even if the layout ever moves out from under
// statePath.
func (p *provisioner) Destroy(ctx context.Context, cluster provision.Cluster, opts ...provision.Option) error {
	options := provision.DefaultOptions()

	for _, opt := range opts {
		if err := opt(&options); err != nil {
			return err
		}
	}

	info := cluster.Info()

	// statePath is the base for the per-node volume dirs (same value create used). It may be unset
	// on a hand-built Cluster; if so we skip the dir cleanup but still stop/remove the containers.
	statePath, statePathErr := cluster.StatePath()

	var errs []error

	for _, node := range info.Nodes {
		fmt.Fprintln(options.LogWriter, "destroying node", node.Name)

		if err := p.stop(ctx, node.ID); err != nil {
			errs = append(errs, err)
		}

		if err := p.remove(ctx, node.ID); err != nil {
			errs = append(errs, err)
		}

		// Remove the node's persistent /var + /system/state host dirs (and their shared parent
		// <statePath>/<nodeName>). RemoveAll ignores a missing path, so this is idempotent.
		if statePathErr == nil {
			varDir, systemStateDir := nodeVolumePaths(statePath, node.Name)

			for _, dir := range []string{varDir, systemStateDir, filepath.Dir(varDir)} {
				if err := os.RemoveAll(dir); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}

	if err := p.destroyNetwork(ctx, info.Network.Name); err != nil {
		errs = append(errs, err)
	}

	// Per-cluster sweep: removes state.yaml and the volume parent dir tree.
	if statePathErr == nil {
		if err := os.RemoveAll(statePath); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
