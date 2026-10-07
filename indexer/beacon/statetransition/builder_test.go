package statetransition

import (
	"github.com/ethpandaops/dora/clients/consensus"
	"github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"testing"
)

func TestBuilderQuorumUsesCommitteeRound(t *testing.T) {
	total := phase0.Gwei(320 * 32_000_000_000)
	for _, round := range []uint64{0, 32, 8} {
		specs := &consensus.ChainSpec{ChainSpecPreset: consensus.ChainSpecPreset{SlotsPerEpoch: 32, SlotsPerRound: round}}
		s := &stateAccessor{BeaconState: &all.BeaconState{}, specs: specs, caches: &stateTransitionCaches{totalActiveBalCache: &total}}
		want := uint64(total) / specs.CommitteeSlotsPerRound() * 6 / 10
		if got := getBuilderPaymentQuorumThreshold(s); got != want {
			t.Fatalf("round %d threshold %d, want %d", round, got, want)
		}
	}
}
