package congestion_test

import (
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
)

// fakeProvider is a minimal slice-backed CongestionProvider used to prove the
// port is satisfiable. The real in-memory/static/kafka adapters land later.
type fakeProvider struct {
	loads congestion.LoadSnapshot // indexed by EdgeID
}

func (p fakeProvider) Load(id domain.EdgeID) float64 { return p.loads.Load(id) }

func (p fakeProvider) Snapshot() congestion.LoadSnapshot {
	out := make(congestion.LoadSnapshot, len(p.loads))
	copy(out, p.loads)
	return out
}

// Compile-time assertion: fakeProvider satisfies the CongestionProvider port.
var _ congestion.CongestionProvider = fakeProvider{}

func TestFakeProviderSatisfiesPort(t *testing.T) {
	// EdgeID 10 carries load 850; everything else is unobserved.
	loads := make(congestion.LoadSnapshot, 11)
	loads[10] = 850
	p := fakeProvider{loads: loads}

	if got := p.Load(10); got != 850 {
		t.Errorf("Load(10) = %v, want 850", got)
	}
	if got := p.Load(5); got != 0 {
		t.Errorf("Load(unobserved) = %v, want 0", got)
	}
	if got := p.Load(999); got != 0 {
		t.Errorf("Load(out of range) = %v, want 0", got)
	}
	if got := p.Load(-1); got != 0 {
		t.Errorf("Load(negative) = %v, want 0", got)
	}

	// Snapshot must be a fresh copy: mutating it must not affect the provider.
	snap := p.Snapshot()
	snap[10] = 0
	if got := p.Load(10); got != 850 {
		t.Errorf("Load(10) after mutating snapshot = %v, want 850 (snapshot must be a copy)", got)
	}
}
