// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package apple

import (
	"net/netip"
	"os"
	"path/filepath"
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
// (it shadows the image's /opt/cni/bin), while the ephemeral propagation/runtime paths must be
// present. It also locks the persistent-volume change: /var and /system/state are NO LONGER tmpfs
// (they are persistent host bind-mounts so cluster state survives a cold restart).
func TestNodeTmpfsPaths_ExcludesOptKeepsCNI(t *testing.T) {
	paths := nodeTmpfsPaths()

	if slices.Contains(paths, "/opt") {
		t.Error("/opt must NOT be tmpfs-mounted (would shadow shipped /opt/cni/bin -> CNI sandbox failure)")
	}

	// /var (etcd) and /system/state (config + PKI) moved to persistent --volume; they must not be tmpfs.
	for _, persistent := range []string{"/var", "/system/state"} {
		if slices.Contains(paths, persistent) {
			t.Errorf("state-bearing path %q must NOT be tmpfs (it is now a persistent --volume; tmpfs wipes it on cold restart)", persistent)
		}
	}

	for _, required := range []string{"/run", "/etc/cni", "/system", "/tmp"} {
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

	args := buildRunArgs(clusterReq, nodeReq)

	joined := strings.Join(args, " ")

	checks := []struct {
		ok   bool
		desc string
	}{
		{hasPair(args, "--cap-add", "ALL"), "--cap-add ALL (G2: machined needs full caps)"},
		{hasPair(args, "--memory", "4096MB"), "--memory in verified MB form"},
		{!hasPair(args, "--tmpfs", "/opt"), "/opt NOT tmpfs (G4)"},
		{hasPair(args, "--tmpfs", "/etc/cni"), "/etc/cni tmpfs present"},
		// Persistent-state change: /var + /system/state must NOT be tmpfs, and MUST be --volume mounts.
		{!hasPair(args, "--tmpfs", "/var"), "/var NOT tmpfs (now a persistent --volume)"},
		{!hasPair(args, "--tmpfs", "/system/state"), "/system/state NOT tmpfs (now a persistent --volume)"},
		{hasVolumeForTarget(args, "/var"), "--volume ...:/var present (persistent etcd data)"},
		{hasVolumeForTarget(args, "/system/state"), "--volume ...:/system/state present (persistent config + PKI)"},
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

// hasVolumeForTarget reports whether args contains a "--volume <host>:<target>" mount for the given
// in-VM target path (the host side is per-cluster/per-node, so we match the trailing ":<target>").
func hasVolumeForTarget(args []string, target string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--volume" && strings.HasSuffix(args[i+1], ":"+target) {
			return true
		}
	}

	return false
}

// TestNodeVolumePaths_Derivation locks the host-path scheme: <statePath>/<nodeName>/{var,system-state}.
// The exact strings are load-bearing — Create mkdir's them, buildRunArgs bind-mounts them, and Destroy
// removes them, so a drift here would silently break either the mount or the cleanup.
func TestNodeVolumePaths_Derivation(t *testing.T) {
	statePath := filepath.Join("_out", "clusters", "aegis")

	varDir, systemStateDir := nodeVolumePaths(statePath, "aegis-controlplane-1")

	wantVar := filepath.Join("_out", "clusters", "aegis", "aegis-controlplane-1", "var")
	wantState := filepath.Join("_out", "clusters", "aegis", "aegis-controlplane-1", "system-state")

	if varDir != wantVar {
		t.Errorf("/var host dir: got %q, want %q", varDir, wantVar)
	}

	if systemStateDir != wantState {
		t.Errorf("/system/state host dir: got %q, want %q", systemStateDir, wantState)
	}

	// The in-VM target for /system/state cannot be a host path component, so the host side uses
	// "system-state" — guard against a regression that reintroduces a slash.
	if strings.Contains(filepath.Base(systemStateDir), "/") {
		t.Errorf("system-state host dir base must not contain a slash: %q", systemStateDir)
	}
}

// TestVolumePaths_CreateDestroySymmetry proves the dirs buildRunArgs bind-mounts are exactly the dirs
// Destroy would remove — both derive from the same clusterStatePath + nodeVolumePaths, so cleanup can
// never target a different (or empty) directory than the one Create populated.
func TestVolumePaths_CreateDestroySymmetry(t *testing.T) {
	clusterReq := provision.ClusterRequest{Name: "aegis", StateDirectory: filepath.Join("_out", "clusters")}
	nodeName := "aegis-worker-1"

	// What buildRunArgs mounts (host side of the --volume args).
	args := buildRunArgs(clusterReq, provision.NodeRequest{Name: nodeName, Type: machine.TypeWorker})

	mounted := map[string]string{} // target -> host

	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--volume" {
			host, target, _ := strings.Cut(args[i+1], ":")
			mounted[target] = host
		}
	}

	// What Destroy would remove (it calls clusterStatePath + nodeVolumePaths on the recorded state).
	statePath := clusterStatePath(clusterReq)
	wantVar, wantState := nodeVolumePaths(statePath, nodeName)

	if mounted["/var"] != wantVar {
		t.Errorf("/var: mounted %q but destroy targets %q", mounted["/var"], wantVar)
	}

	if mounted["/system/state"] != wantState {
		t.Errorf("/system/state: mounted %q but destroy targets %q", mounted["/system/state"], wantState)
	}
}

// TestPrepareNodeVolumes_StaleStateGuard is the BVA on the "is the /var volume dir empty?" boundary
// (CLAUDE.md k). B = 0 entries. B-1 (dir absent) and B (present, empty) must pass and create the
// dirs; B+1 (>= 1 entry, stale state) must be rejected so we never boot onto old etcd/PKI.
func TestPrepareNodeVolumes_StaleStateGuard(t *testing.T) {
	node := workerReq("aegis-worker-1")
	reqs := []provision.NodeRequest{node}

	t.Run("absent dir (B-1): allowed, dirs created", func(t *testing.T) {
		statePath := t.TempDir() // node dir does not exist yet

		if err := prepareNodeVolumes(statePath, reqs); err != nil {
			t.Fatalf("absent volume dir must be allowed: %v", err)
		}

		varDir, systemStateDir := nodeVolumePaths(statePath, node.Name)
		for _, dir := range []string{varDir, systemStateDir} {
			if _, err := os.Stat(dir); err != nil {
				t.Errorf("expected %q created: %v", dir, err)
			}
		}
	})

	t.Run("present empty dir (B): allowed", func(t *testing.T) {
		statePath := t.TempDir()
		varDir, _ := nodeVolumePaths(statePath, node.Name)

		if err := os.MkdirAll(varDir, 0o755); err != nil {
			t.Fatal(err)
		}

		if err := prepareNodeVolumes(statePath, reqs); err != nil {
			t.Fatalf("present-but-empty volume dir must be allowed: %v", err)
		}
	})

	t.Run("non-empty dir (B+1): rejected", func(t *testing.T) {
		statePath := t.TempDir()
		varDir, _ := nodeVolumePaths(statePath, node.Name)

		if err := os.MkdirAll(varDir, 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(varDir, "etcd-leftover"), []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}

		if err := prepareNodeVolumes(statePath, reqs); err == nil {
			t.Error("non-empty (stale) volume dir must be rejected, telling the operator to destroy first")
		}
	})
}
