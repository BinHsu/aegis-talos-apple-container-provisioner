// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Command aegis is a thin driver that provisions a Talos cluster on Apple's `container`
// runtime via the apple provider. It exists because choice A keeps the provider as an
// out-of-tree module (we do not fork talosctl), so we cannot register a `talosctl cluster
// create apple` subcommand to run it.
//
// It deliberately mirrors what `talosctl cluster create` does in-process — build the config
// bundle through the provider's GenOptions, then call provision.Provisioner.Create — so it is
// the direct precursor of the eventual upstream cmd_apple.go. Bootstrap / health / kubeconfig
// are left to the operator (as talosctl's postCreate does), and printed as next steps.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/netip"
	"path/filepath"
	"strings"

	"github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/bundle"
	"github.com/siderolabs/talos/pkg/machinery/config/generate"
	"github.com/siderolabs/talos/pkg/machinery/config/machine"
	"github.com/siderolabs/talos/pkg/machinery/constants"
	"github.com/siderolabs/talos/pkg/provision"

	"github.com/BinHsu/aegis-talos-apple-container-provisioner/provider/apple"
)

const mib = 1024 * 1024

func main() {
	if err := run(); err != nil {
		log.Fatalf("aegis: %v", err)
	}
}

func run() error {
	var (
		clusterName = flag.String("name", "aegis", "cluster name")
		talosImage  = flag.String("image", "ghcr.io/siderolabs/talos:v1.13.3", "Talos node image")
		kubeVersion = flag.String("kube-version", "", "Kubernetes version (empty = Talos default)")
		stateDir    = flag.String("state-dir", "_out/clusters", "cluster state directory")
		subnet      = flag.String("subnet", "192.168.64.0/24", "vmnet subnet (informational; DHCP assigns IPs)")
		cpMemMB     = flag.Int64("cp-memory", 4096, "control-plane memory (MB); >= ~2GB required")
		workerMemMB = flag.Int64("worker-memory", 2048, "worker memory (MB)")
		cpCount     = flag.Int("controlplanes", 1, "number of control-plane nodes")
		workerCount = flag.Int("workers", 1, "number of worker nodes")
		destroy     = flag.Bool("destroy", false, "destroy the named cluster (Reflect + Destroy) instead of creating it")
		dnsDomain   = flag.String("dns-domain", "aegis", "Apple container DNS domain for stable FQDN node names "+
			"(<node>.<domain>); set to \"\" to disable FQDN naming and fall back to IP-only (v0.1.x). "+
			"Prerequisite: sudo container system dns create <domain> (must re-run after macOS reboot).")
	)

	flag.Parse()

	ctx := context.Background()

	prov, err := apple.NewProvisioner(ctx, apple.Config{DNSDomain: *dnsDomain})
	if err != nil {
		return err
	}

	defer prov.Close() //nolint:errcheck

	if *destroy {
		return runDestroy(ctx, prov, *clusterName, *stateDir)
	}

	// Talos version contract derived from the image tag (e.g. ...:v1.13.3 -> v1.13.3).
	talosVersion := *talosImage
	if i := strings.LastIndex(talosVersion, ":"); i != -1 {
		talosVersion = talosVersion[i+1:]
	}

	versionContract, err := config.ParseContractFromVersion(talosVersion)
	if err != nil {
		return fmt.Errorf("parsing Talos version %q: %w", talosVersion, err)
	}

	cidr, err := netip.ParsePrefix(*subnet)
	if err != nil {
		return fmt.Errorf("parsing subnet: %w", err)
	}

	// vmnet gateway is the first address in the subnet (verified G4: 192.168.64.1).
	gateway := cidr.Addr().Next()

	networkReq := provision.NetworkRequest{
		Name:         "default", // built-in vmnet network; ensureNetwork uses it as-is
		CIDRs:        []netip.Prefix{cidr},
		GatewayAddrs: []netip.Addr{gateway},
		MTU:          1500,
	}

	// Build the config bundle the same way talosctl cluster create does: ask the provider for its
	// gen/bundle options (this is what exercises apple.GenOptions), then add the input options.
	inClusterEndpoint := prov.GetInClusterKubernetesControlPlaneEndpoint(networkReq, constants.DefaultControlPlanePort)

	genOpts, bundleOpts := prov.GenOptions(networkReq, versionContract)
	genOpts = append(genOpts,
		generate.WithVersionContract(versionContract),
		generate.WithEndpointList(prov.GetTalosAPIEndpoints(networkReq)),
	)

	bundleOpts = append(bundleOpts, bundle.WithInputOptions(&bundle.InputOptions{
		ClusterName: *clusterName,
		Endpoint:    inClusterEndpoint,
		KubeVersion: strings.TrimPrefix(*kubeVersion, "v"),
		GenOptions:  genOpts,
	}))

	configBundle, err := bundle.NewBundle(bundleOpts...)
	if err != nil {
		return fmt.Errorf("generating config bundle: %w", err)
	}

	nodes := make([]provision.NodeRequest, 0, *cpCount+*workerCount)

	for i := range *cpCount {
		nodes = append(nodes, provision.NodeRequest{
			Name:     fmt.Sprintf("%s-controlplane-%d", *clusterName, i+1),
			Type:     machine.TypeControlPlane,
			Config:   configBundle.ControlPlane(),
			Memory:   *cpMemMB * mib,
			NanoCPUs: 2e9,
		})
	}

	for i := range *workerCount {
		nodes = append(nodes, provision.NodeRequest{
			Name:     fmt.Sprintf("%s-worker-%d", *clusterName, i+1),
			Type:     machine.TypeWorker,
			Config:   configBundle.Worker(),
			Memory:   *workerMemMB * mib,
			NanoCPUs: 2e9,
		})
	}

	clusterReq := provision.ClusterRequest{
		Name:           *clusterName,
		Network:        networkReq,
		Nodes:          nodes,
		Image:          *talosImage,
		StateDirectory: *stateDir,
		SelfExecutable: "talosctl", // apply-config is run via talosctl (see reconcile.go)
	}

	cluster, err := prov.Create(ctx, clusterReq, provision.WithTalosConfig(configBundle.TalosConfig()))
	if err != nil {
		return err
	}

	// Write the cluster's talosconfig so the operator can drive bootstrap/health.
	talosconfigPath := filepath.Join(*stateDir, *clusterName, "talosconfig")
	if err = configBundle.TalosConfig().Save(talosconfigPath); err != nil {
		return fmt.Errorf("saving talosconfig: %w", err)
	}

	reportProvisioned(cluster.Info(), *dnsDomain, talosconfigPath)

	return nil
}

// runDestroy tears down a named cluster. It tolerates a missing state.yaml (a Create that failed
// before saveState, which may leave a stuck container + named volumes) by falling back to a
// label-based sweep keyed on the cluster name; other Reflect errors still surface.
func runDestroy(ctx context.Context, prov provision.Provisioner, clusterName, stateDir string) error {
	cluster, err := prov.Reflect(ctx, clusterName, stateDir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("reflecting cluster %q: %w", clusterName, err)
		}

		statePath := filepath.Join(stateDir, clusterName)
		fmt.Printf("no state.yaml for cluster %q; sweeping by label %s\n", clusterName, "talos.cluster.name="+clusterName)
		cluster = apple.ClusterRef(clusterName, statePath)
	}

	if err = prov.Destroy(ctx, cluster); err != nil {
		return fmt.Errorf("destroying cluster %q: %w", clusterName, err)
	}

	fmt.Printf("destroyed cluster %q\n", clusterName)

	return nil
}

// reportProvisioned prints the provisioned nodes and the operator's next steps. The control-plane
// endpoint is the FQDN when a DNS domain is configured (stable across cold restarts), else the
// current DHCP IP.
func reportProvisioned(info provision.ClusterInfo, dnsDomain, talosconfigPath string) {
	fmt.Println("\n=== cluster provisioned ===")

	var cpEndpoint string

	for _, n := range info.Nodes {
		role := "worker"
		if n.Type == machine.TypeControlPlane || n.Type == machine.TypeInit {
			role = "controlplane"

			if cpEndpoint == "" {
				if dnsDomain != "" {
					cpEndpoint = n.ID // FQDN (e.g. "aegis-controlplane-1.aegis")
				} else if len(n.IPs) > 0 {
					cpEndpoint = n.IPs[0].String()
				}
			}
		}

		if len(n.IPs) > 0 {
			fmt.Printf("  %-28s %-12s %s\n", n.Name, role, n.IPs[0])
		}
	}

	fmt.Printf("\ntalosconfig: %s\n", talosconfigPath)
	fmt.Println("\nnext steps (operator):")
	fmt.Printf("  export TALOSCONFIG=%s\n", talosconfigPath)
	fmt.Printf("  talosctl config endpoint %s && talosctl config node %s\n", cpEndpoint, cpEndpoint)
	fmt.Printf("  talosctl bootstrap\n")
	fmt.Printf("  talosctl health\n")
	fmt.Printf("  talosctl kubeconfig ./kubeconfig && KUBECONFIG=./kubeconfig kubectl get nodes\n")
}
