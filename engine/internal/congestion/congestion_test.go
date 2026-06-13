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

func (provider fakeProvider) Load(edgeID domain.EdgeID) float64 { return provider.loads.Load(edgeID) }

func (provider fakeProvider) Snapshot() congestion.LoadSnapshot {
	out := make(congestion.LoadSnapshot, len(provider.loads))
	copy(out, provider.loads)
	return out
}

// Compile-time assertion: fakeProvider satisfies the CongestionProvider port.
var _ congestion.CongestionProvider = fakeProvider{}

func TestFakeProviderSatisfiesPort(test *testing.T) {
	// EdgeID 10 carries load 850; everything else is unobserved.
	loads := make(congestion.LoadSnapshot, 11)
	loads[10] = 850
	provider := fakeProvider{loads: loads}

	if got := provider.Load(10); got != 850 {
		test.Errorf("Load(10) = %v, want 850", got)
	}
	if got := provider.Load(5); got != 0 {
		test.Errorf("Load(unobserved) = %v, want 0", got)
	}
	if got := provider.Load(999); got != 0 {
		test.Errorf("Load(out of range) = %v, want 0", got)
	}
	if got := provider.Load(-1); got != 0 {
		test.Errorf("Load(negative) = %v, want 0", got)
	}

	// Snapshot must be a fresh copy: mutating it must not affect the provider.
	snap := provider.Snapshot()
	snap[10] = 0
	if got := provider.Load(10); got != 850 {
		test.Errorf("Load(10) after mutating snapshot = %v, want 850 (snapshot must be a copy)", got)
	}
}
