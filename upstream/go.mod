// This nested module marks upstream/ as NOT part of the parent module's build.
//
// The files under upstream/ are the exact PR delta for siderolabs/talos, at their in-tree paths.
// They reference talos internal/ packages and talosctl command-package symbols, so they only
// compile inside a talos checkout (verified in _out/talos-fork; see upstream/README.md), never
// standalone. The nested go.mod keeps the parent module's `go build ./...` / tests / lint from
// trying — and failing — to compile them, while preserving them as real .go files for review and
// mechanical copy into the talos tree.
module github.com/BinHsu/aegis-apple-container-provisioner-talos/upstream

go 1.26.4
