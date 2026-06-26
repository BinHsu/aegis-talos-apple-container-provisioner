// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package apple

import (
	"context"
	"errors"
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
		// Persistent-state change: /var + /system/state must NOT be tmpfs, and MUST be NAMED-VOLUME mounts.
		{!hasPair(args, "--tmpfs", "/var"), "/var NOT tmpfs (now a persistent named volume)"},
		{!hasPair(args, "--tmpfs", "/system/state"), "/system/state NOT tmpfs (now a persistent named volume)"},
		{hasVolumeForTarget(args, "/var"), "--volume ...:/var present (persistent etcd data)"},
		{hasVolumeForTarget(args, "/system/state"), "--volume ...:/system/state present (persistent config + PKI)"},
		// The volume SOURCE must be a named volume, not a host path: a host-path bind-mount is a
		// virtio-fs share the guest cannot chmod, which breaks Talos boot (the verified G5 failure).
		{isNamedVolumeSource(args, "/var"), "--volume source for /var is a named volume (no '/'), not a host path"},
		{isNamedVolumeSource(args, "/system/state"), "--volume source for /system/state is a named volume (no '/'), not a host path"},
		{volumeSource(args, "/var") == "aegis-aegis-controlplane-1-var", "/var volume name is <cluster>-<node>-var"},
		{volumeSource(args, "/system/state") == "aegis-aegis-controlplane-1-system-state", "/system/state volume name is <cluster>-<node>-system-state"},
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

// hasVolumeForTarget reports whether args contains a "--volume <source>:<target>" mount for the given
// in-VM target path (the source is per-cluster/per-node, so we match the trailing ":<target>").
func hasVolumeForTarget(args []string, target string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--volume" && strings.HasSuffix(args[i+1], ":"+target) {
			return true
		}
	}

	return false
}

// volumeSource returns the source (left of ':') of the "--volume <source>:<target>" mount for target,
// or "" if there is none.
func volumeSource(args []string, target string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--volume" && strings.HasSuffix(args[i+1], ":"+target) {
			src, _, _ := strings.Cut(args[i+1], ":")

			return src
		}
	}

	return ""
}

// isNamedVolumeSource reports whether the --volume mount for target uses a NAMED-VOLUME source (a bare
// name) rather than a host PATH (which would contain a '/'). The named-vs-path distinction is the whole
// point of this change: a host-path bind-mount is the virtio-fs share Talos cannot chmod.
func isNamedVolumeSource(args []string, target string) bool {
	src := volumeSource(args, target)

	return src != "" && !strings.Contains(src, "/")
}

// TestNodeVolumeNames_Derivation locks the named-volume scheme: <cluster>-<node>-{var,system-state},
// sanitized. The exact strings are load-bearing — buildRunArgs mounts them, Create creates them, and
// Destroy deletes them, so a drift here would silently break either the mount or the cleanup.
func TestNodeVolumeNames_Derivation(t *testing.T) {
	varVol, systemStateVol := nodeVolumeNames("aegis", "aegis-controlplane-1")

	if want := "aegis-aegis-controlplane-1-var"; varVol != want {
		t.Errorf("/var volume name: got %q, want %q", varVol, want)
	}

	if want := "aegis-aegis-controlplane-1-system-state"; systemStateVol != want {
		t.Errorf("/system/state volume name: got %q, want %q", systemStateVol, want)
	}

	// A volume name cannot contain a slash (it is not a host path) — guard against a regression that
	// reintroduces one or any other invalid character.
	for _, name := range []string{varVol, systemStateVol} {
		if strings.ContainsAny(name, "/ ") {
			t.Errorf("volume name must contain no slash or space: %q", name)
		}
	}

	// Sanitization: an uppercase cluster name with invalid chars must become a valid lowercase name.
	dirty, _ := nodeVolumeNames("My_Cluster!", "Node/1")
	if want := "my-cluster--node-1-var"; dirty != want {
		t.Errorf("sanitized name: got %q, want %q", dirty, want)
	}
}

// TestVolumeNames_CreateDestroySymmetry proves the volumes buildRunArgs mounts are exactly the volumes
// Destroy would delete — both derive from the same nodeVolumeNames(clusterName, nodeName), so cleanup
// can never target a different volume than the one Create provisioned.
func TestVolumeNames_CreateDestroySymmetry(t *testing.T) {
	clusterReq := provision.ClusterRequest{Name: "aegis"}
	nodeName := "aegis-worker-1"

	// What buildRunArgs mounts (source side of the --volume args).
	args := buildRunArgs(clusterReq, provision.NodeRequest{Name: nodeName, Type: machine.TypeWorker})

	mounted := map[string]string{} // target -> source

	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--volume" {
			src, target, _ := strings.Cut(args[i+1], ":")
			mounted[target] = src
		}
	}

	// What Destroy would delete (it calls nodeVolumeNames on the recorded ClusterName + node name).
	wantVar, wantState := nodeVolumeNames(clusterReq.Name, nodeName)

	if mounted["/var"] != wantVar {
		t.Errorf("/var: mounted %q but destroy targets %q", mounted["/var"], wantVar)
	}

	if mounted["/system/state"] != wantState {
		t.Errorf("/system/state: mounted %q but destroy targets %q", mounted["/system/state"], wantState)
	}
}

// TestPrepareNodeVolumes_StaleStateGuard exercises the stale-state boundary now that state lives in a
// named volume. The boundary is the boolean "does this volume already EXIST?" — there is no B-1/B/B+1
// triple for a boolean, so per CLAUDE.md (k) we test BOTH sides of the boundary (absent -> created;
// present -> rejected) plus the error path. Stale (existing) volumes must never be silently reused.
func TestPrepareNodeVolumes_StaleStateGuard(t *testing.T) {
	node := workerReq("aegis-worker-1")
	reqs := []provision.NodeRequest{node}
	const clusterName = "aegis"

	t.Run("no existing volume (clean): allowed, both volumes created", func(t *testing.T) {
		var created []string

		exists := func(_ context.Context, _ string) (bool, error) { return false, nil }
		create := func(_ context.Context, name string) error { created = append(created, name); return nil }

		if err := prepareNodeVolumes(context.Background(), clusterName, reqs, exists, create); err != nil {
			t.Fatalf("clean state must be allowed: %v", err)
		}

		wantVar, wantState := nodeVolumeNames(clusterName, node.Name)
		if !slices.Contains(created, wantVar) || !slices.Contains(created, wantState) {
			t.Errorf("expected both volumes created (%q, %q), got %v", wantVar, wantState, created)
		}
	})

	t.Run("existing volume (stale): rejected, nothing created", func(t *testing.T) {
		createCalled := false

		exists := func(_ context.Context, _ string) (bool, error) { return true, nil }
		create := func(_ context.Context, _ string) error { createCalled = true; return nil }

		if err := prepareNodeVolumes(context.Background(), clusterName, reqs, exists, create); err == nil {
			t.Error("existing (stale) volume must be rejected, telling the operator to destroy first")
		}

		if createCalled {
			t.Error("must NOT create a volume when a stale one already exists")
		}
	})

	t.Run("existence-check error propagates", func(t *testing.T) {
		exists := func(_ context.Context, _ string) (bool, error) { return false, errors.New("cli boom") }
		create := func(_ context.Context, _ string) error { return nil }

		if err := prepareNodeVolumes(context.Background(), clusterName, reqs, exists, create); err == nil {
			t.Error("an error from the existence check must propagate, not be swallowed")
		}
	})
}

// TestClusterLabelSelector locks the exact selector string the destroy sweep builds. The literal
// "talos.cluster.name=<name>" is load-bearing: it must match the --label buildRunArgs stamps on
// containers and the labels volumeCreate stamps on volumes, or the sweep finds nothing.
func TestClusterLabelSelector(t *testing.T) {
	if got, want := clusterLabelSelector("aegis"), "talos.cluster.name=aegis"; got != want {
		t.Errorf("selector: got %q, want %q", got, want)
	}

	// The container label buildRunArgs writes must equal the selector, or the sweep can't find them.
	args := buildRunArgs(provision.ClusterRequest{Name: "aegis"}, workerReq("aegis-worker-1"))
	if !hasPair(args, "--label", clusterLabelSelector("aegis")) {
		t.Errorf("container is not labeled with the destroy selector %q; sweep would miss it\nargs: %s",
			clusterLabelSelector("aegis"), strings.Join(args, " "))
	}
}

// TestVolumeLabels_MatchSelector proves volume creation labels line up with the destroy selector:
// volumeLabels(name)[0] must equal clusterLabelSelector(name), so a volume the sweep is looking for is
// exactly the one create stamped. Also checks the owned marker is present.
func TestVolumeLabels_MatchSelector(t *testing.T) {
	labels := volumeLabels("aegis")

	if len(labels) == 0 || labels[0] != clusterLabelSelector("aegis") {
		t.Errorf("volumeLabels[0]=%v must equal selector %q", labels, clusterLabelSelector("aegis"))
	}

	if !slices.Contains(labels, "talos.owned=true") {
		t.Errorf("volume must carry talos.owned=true, got %v", labels)
	}
}

// TestVolumeCreateArgs asserts the `volume create` vector carries each label behind its own --label
// flag and keeps <name> as the trailing positional argument (a name in the wrong position would make
// the CLI treat a label as the name). The no-label case must still produce a valid command.
func TestVolumeCreateArgs(t *testing.T) {
	args := volumeCreateArgs("aegis-w1-var", "talos.cluster.name=aegis", "talos.owned=true")

	if !hasPair(args, "--label", "talos.cluster.name=aegis") || !hasPair(args, "--label", "talos.owned=true") {
		t.Errorf("both labels must appear behind --label: %v", args)
	}

	if args[0] != "volume" || args[1] != "create" {
		t.Errorf("command must start with `volume create`: %v", args)
	}

	if args[len(args)-1] != "aegis-w1-var" {
		t.Errorf("volume name must be the trailing positional arg: %v", args)
	}

	// Boundary: zero labels still yields a valid `volume create <name>`.
	if bare := volumeCreateArgs("v0"); len(bare) != 3 || bare[2] != "v0" {
		t.Errorf("no-label create must be `volume create v0`: %v", bare)
	}
}

// TestContainersMatchingLabel exercises the client-side label filter (the CLI has no native filter).
// The sample JSON mirrors the real `container list --all --format json` schema. Cases: exact match
// (included), same key with a different value (excluded), label absent (excluded), and an empty list.
func TestContainersMatchingLabel(t *testing.T) {
	const sample = `[
	  {"configuration":{"id":"aegis-cp-1","labels":{"talos.cluster.name":"aegis","talos.owned":"true"}}},
	  {"configuration":{"id":"other-cp-1","labels":{"talos.cluster.name":"other","talos.owned":"true"}}},
	  {"configuration":{"id":"unlabeled","labels":{}}}
	]`

	got, err := containersMatchingLabel(sample, clusterLabelSelector("aegis"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if want := []string{"aegis-cp-1"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v (must include only the aegis-labeled container)", got, want)
	}

	empty, err := containersMatchingLabel(`[]`, clusterLabelSelector("aegis"))
	if err != nil {
		t.Fatalf("empty list must not error: %v", err)
	}

	if len(empty) != 0 {
		t.Errorf("empty list must yield no matches, got %v", empty)
	}

	if _, err := containersMatchingLabel(sample, "no-equals-sign"); err == nil {
		t.Error("a selector without '=' must be rejected")
	}
}

// TestVolumesMatchingLabel mirrors TestContainersMatchingLabel for the volume schema, where the match
// returns `.configuration.name` rather than `.configuration.id`.
func TestVolumesMatchingLabel(t *testing.T) {
	const sample = `[
	  {"configuration":{"name":"aegis-w1-var","labels":{"talos.cluster.name":"aegis","talos.owned":"true"}}},
	  {"configuration":{"name":"aegis-w1-system-state","labels":{"talos.cluster.name":"aegis"}}},
	  {"configuration":{"name":"other-var","labels":{"talos.cluster.name":"other"}}},
	  {"configuration":{"name":"legacy-unlabeled","labels":{}}}
	]`

	got, err := volumesMatchingLabel(sample, clusterLabelSelector("aegis"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"aegis-w1-var", "aegis-w1-system-state"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v (only aegis-labeled volumes)", got, want)
	}
}

// TestClusterRef_CarriesNameForSweep proves the missing-state fallback (cmd/aegis) hands Destroy a
// Cluster whose ClusterName drives the label sweep and whose StatePath is the per-cluster dir, with an
// empty node list (there is no recorded state when Create failed before saveState).
func TestClusterRef_CarriesNameForSweep(t *testing.T) {
	c := ClusterRef("aegis", "_out/clusters/aegis")

	if c.Info().ClusterName != "aegis" {
		t.Errorf("ClusterName: got %q, want %q", c.Info().ClusterName, "aegis")
	}

	if len(c.Info().Nodes) != 0 {
		t.Errorf("missing-state ref must have no recorded nodes, got %d", len(c.Info().Nodes))
	}

	sp, err := c.StatePath()
	if err != nil || sp != "_out/clusters/aegis" {
		t.Errorf("StatePath: got %q, err %v", sp, err)
	}

	// The sweep selector derived from this ref must equal what create stamps.
	if clusterLabelSelector(c.Info().ClusterName) != "talos.cluster.name=aegis" {
		t.Errorf("ref-derived selector drifted: %q", clusterLabelSelector(c.Info().ClusterName))
	}
}
