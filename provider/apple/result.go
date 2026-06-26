// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package apple

import (
	"errors"

	"github.com/siderolabs/talos/pkg/provision"
)

// result implements provision.Cluster. Mirrors the docker provider's result type.
type result struct {
	clusterInfo provision.ClusterInfo

	statePath string
}

func (res *result) Provisioner() string {
	return ProviderName
}

func (res *result) Info() provision.ClusterInfo {
	return res.clusterInfo
}

func (res *result) StatePath() (string, error) {
	if res.statePath == "" {
		return "", errors.New("state path is not set")
	}

	return res.statePath, nil
}

// ClusterRef builds a minimal provision.Cluster carrying only the cluster name and state path. It
// exists for the destroy path when state.yaml is absent — a Create that failed before saveState leaves
// orphaned containers/volumes but no recorded node list. Info().Nodes is empty, so Destroy relies
// entirely on its label sweep (keyed on ClusterName) to reclaim those resources, then removes statePath.
func ClusterRef(clusterName, statePath string) provision.Cluster {
	return &result{
		clusterInfo: provision.ClusterInfo{ClusterName: clusterName},
		statePath:   statePath,
	}
}
