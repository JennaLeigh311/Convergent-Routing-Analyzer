package congestion_test

import (
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
)

// fakeProvider is a minimal map-backed CongestionProvider used to prove the
// port is satisfiable. The real in-memory/static/kafka adapters land later.
type fakeProvider struct {
	loads map[domain.EdgeID]float64
}

func (p fakeProvider) Load(id domain.EdgeID) float64 { return p.loads[id] }

func (p fakeProvider) Snapshot() map[domain.EdgeID]float64 {
	out := make(map[domain.EdgeID]float64, len(p.loads))
	for k, v := range p.loads {
		out[k] = v
	}
	return out
}

// Compile-time assertion: fakeProvider satisfies the CongestionProvider port.
var _ congestion.CongestionProvider = fakeProvider{}

func TestFakeProviderSatisfiesPort(t *testing.T) {
	p := fakeProvider{loads: map[domain.EdgeID]float64{10: 850}}

	if got := p.Load(10); got != 850 {
		t.Errorf("Load(10) = %v, want 850", got)
	}
	if got := p.Load(999); got != 0 {
		t.Errorf("Load(unknown) = %v, want 0", got)
	}

	// Snapshot must be a copy: mutating it must not affect the provider.
	snap := p.Snapshot()
	snap[10] = 0
	if got := p.Load(10); got != 850 {
		t.Errorf("Load(10) after mutating snapshot = %v, want 850 (snapshot must be a copy)", got)
	}
}
