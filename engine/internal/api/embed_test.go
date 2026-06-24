package api

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestEmbeddedGraphMatchesTestdata pins the invariant the server.go embed comment
// claims: engine/internal/api/toy_network.geojson is a VERBATIM copy of the
// canonical engine/testdata/toy_network.geojson. The file is duplicated because
// the loader takes a reader and the distroless container ships no testdata tree,
// so the binary must embed its own copy — but a copy silently drifts if the
// canonical fixture is regenerated (e.g. an #81-style length fix). This test makes
// the divergence a CI failure instead of a stale embedded graph shipping unnoticed.
func TestEmbeddedGraphMatchesTestdata(t *testing.T) {
	embedded, err := toyNetworkFS.ReadFile(toyNetworkName)
	if err != nil {
		t.Fatalf("read embedded %q: %v", toyNetworkName, err)
	}
	canonicalPath := filepath.Join("..", "..", "testdata", toyNetworkName)
	canonical, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("read canonical %q: %v", canonicalPath, err)
	}
	if !bytes.Equal(embedded, canonical) {
		t.Errorf("embedded %s has drifted from %s; re-copy the canonical fixture so the embedded graph stays verbatim",
			toyNetworkName, canonicalPath)
	}
}
