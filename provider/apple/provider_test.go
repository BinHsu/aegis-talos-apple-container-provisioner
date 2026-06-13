// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package apple

import (
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/siderolabs/talos/pkg/machinery/config/machine"
	"github.com/siderolabs/talos/pkg/provision"
)

func cpReq(name string) provision.NodeRequest {
	return provision.NodeRequest{Name: name, Type: machine.TypeControlPlane}
}

func workerReq(name string) provision.NodeRequest {
	return provision.NodeRequest{Name: name, Type: machine.TypeWorker}
}

// TestValidateClusterRequest_NodeCountBoundaries exercises the control-plane-count boundary
// (BVA, CLAUDE.md k): B = 1 required control-plane. B-1 = 0 must be rejected; B = 1 and above
// accepted. 0 workers (a single control-plane cluster) is valid.
func TestValidateClusterRequest_NodeCountBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		nodes   provision.NodeRequests
		wantErr bool
	}{
		{"no nodes at all", provision.NodeRequests{}, true},
		{"0 control-plane, 1 worker (B-1, invalid)", provision.NodeRequests{workerReq("w1")}, true},
		{"1 control-plane, 0 worker (single-node, valid)", provision.NodeRequests{cpReq("cp1")}, false},
		{"1 control-plane + 1 worker (smallest real, valid)", provision.NodeRequests{cpReq("cp1"), workerReq("w1")}, false},
		{"3 control-plane (valid)", provision.NodeRequests{cpReq("cp1"), cpReq("cp2"), cpReq("cp3")}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateClusterRequest(provision.ClusterRequest{Name: "test", Nodes: tt.nodes})
			if (err != nil) != tt.wantErr {
				t.Fatalf("wantErr=%v, got err=%v", tt.wantErr, err)
			}
		})
	}
}

// TestAssertDistinctIPs guards the everyday "every container gets the same IP" bug.
func TestAssertDistinctIPs(t *testing.T) {
	mk := func(name, ip string) provision.NodeInfo {
		return provision.NodeInfo{Name: name, IPs: []netip.Addr{netip.MustParseAddr(ip)}}
	}

	if err := assertDistinctIPs([]provision.NodeInfo{mk("a", "192.168.64.20"), mk("b", "192.168.64.21")}); err != nil {
		t.Errorf("distinct IPs should pass: %v", err)
	}

	if err := assertDistinctIPs([]provision.NodeInfo{mk("a", "192.168.64.20"), mk("b", "192.168.64.20")}); err == nil {
		t.Error("duplicate IPs must be rejected")
	}
}

// TestNodeTmpfsPaths_ExcludesOptKeepsCNI locks in the G4 finding: /opt must not be tmpfs
// (it shadows the image's /opt/cni/bin), while the propagation/runtime paths must be present.
func TestNodeTmpfsPaths_ExcludesOptKeepsCNI(t *testing.T) {
	paths := nodeTmpfsPaths()

	if slices.Contains(paths, "/opt") {
		t.Error("/opt must NOT be tmpfs-mounted (would shadow shipped /opt/cni/bin -> CNI sandbox failure)")
	}

	for _, required := range []string{"/run", "/var", "/etc/cni", "/system", "/system/state"} {
		if !slices.Contains(paths, required) {
			t.Errorf("required tmpfs path %q missing", required)
		}
	}
}

// TestBuildRunArgs_Recipe locks in the verified G1-G4 launch recipe.
func TestBuildRunArgs_Recipe(t *testing.T) {
	clusterReq := provision.ClusterRequest{
		Name:    "aegis",
		Image:   "ghcr.io/siderolabs/talos:v1.13.3",
		Network: provision.NetworkRequest{Name: "default"},
	}
	nodeReq := provision.NodeRequest{
		Name:     "aegis-controlplane-1",
		Type:     machine.TypeControlPlane,
		Memory:   4096 * 1024 * 1024,
		NanoCPUs: 2e9,
	}

	args, err := buildRunArgs(clusterReq, nodeReq)
	if err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(args, " ")

	checks := []struct {
		ok   bool
		desc string
	}{
		{hasPair(args, "--cap-add", "ALL"), "--cap-add ALL (G2: machined needs full caps)"},
		{hasPair(args, "--memory", "4096MB"), "--memory in verified MB form"},
		{!hasPair(args, "--tmpfs", "/opt"), "/opt NOT tmpfs (G4)"},
		{hasPair(args, "--tmpfs", "/etc/cni"), "/etc/cni tmpfs present"},
		{!strings.Contains(joined, "USERDATA"), "no USERDATA env (apple DHCP divergence from docker)"},
		{hasPair(args, "--env", "PLATFORM=container"), "PLATFORM=container env"},
		{hasPair(args, "--name", "aegis-controlplane-1"), "--name"},
		{hasPair(args, "--network", "default"), "--network"},
		{slices.Contains(args, "--detach"), "--detach"},
		{len(args) > 0 && args[len(args)-1] == clusterReq.Image, "image is the trailing positional arg"},
	}

	for _, c := range checks {
		if !c.ok {
			t.Errorf("buildRunArgs recipe check failed: %s\nargs: %s", c.desc, joined)
		}
	}
}

// hasPair reports whether args contains flag immediately followed by value.
func hasPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}

	return false
}
