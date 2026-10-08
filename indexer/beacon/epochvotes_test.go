package beacon

import (
	offbits "github.com/OffchainLabs/go-bitfield"
	"github.com/ethpandaops/go-eth2-client/spec"
	"github.com/ethpandaops/go-eth2-client/spec/all"
	"testing"

	"github.com/ethpandaops/dora/indexer/beacon/duties"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/prysmaticlabs/go-bitfield"
)

// TestAggregateVotes_SlashedExcludedFromTarget verifies that slashed validators contribute to the
// total vote amount but are reported separately so their weight can be dropped from the FFG target,
// mirroring the consensus spec's get_unslashed_participating_indices.
func TestAggregateVotes_SlashedExcludedFromTarget(t *testing.T) {
	const effBalanceEth = uint32(32)

	epochStatsValues := &EpochStatsValues{
		ActiveValidators:  4,
		ActiveIndices:     []phase0.ValidatorIndex{10, 11, 12, 13},
		EffectiveBalances: []uint32{effBalanceEth, effBalanceEth, effBalanceEth, effBalanceEth},
		AttesterDuties: [][][]duties.ActiveIndiceIndex{
			{ // slot 0
				{0, 1, 2, 3}, // committee 0 -> active-indice positions
			},
		},
	}

	// active-indice positions 1 and 2 are slashed
	slashedSet := map[duties.ActiveIndiceIndex]bool{1: true, 2: true}

	// all four committee members attested
	aggregationBits := bitfield.NewBitlist(4)
	for i := uint64(0); i < 4; i++ {
		aggregationBits.SetBitAt(i, true)
	}

	activityBitlist := bitfield.NewBitlist(epochStatsValues.ActiveValidators)
	votes := &EpochVotes{}

	voteAmount, slashedVoteAmount, committeeSize := votes.aggregateVotes(
		epochStatsValues, 0, 0, aggregationBits, 0, &activityBitlist, slashedSet, func(phase0.ValidatorIndex) {},
	)

	wantTotal := phase0.Gwei(4*effBalanceEth) * EtherGweiFactor
	wantSlashed := phase0.Gwei(2*effBalanceEth) * EtherGweiFactor
	wantTarget := wantTotal - wantSlashed

	if committeeSize != 4 {
		t.Errorf("committeeSize = %d, want 4", committeeSize)
	}
	if voteAmount != wantTotal {
		t.Errorf("voteAmount = %d, want %d", voteAmount, wantTotal)
	}
	if slashedVoteAmount != wantSlashed {
		t.Errorf("slashedVoteAmount = %d, want %d", slashedVoteAmount, wantSlashed)
	}
	if got := voteAmount - slashedVoteAmount; got != wantTarget {
		t.Errorf("target amount = %d, want %d (only the 2 unslashed validators)", got, wantTarget)
	}
}

// TestAggregateVotes_NilSlashedSet ensures a nil slashed set (the common case) counts every vote
// towards the target with no exclusions and does not panic on lookup.
func TestAggregateVotes_NilSlashedSet(t *testing.T) {
	epochStatsValues := &EpochStatsValues{
		ActiveValidators:  2,
		ActiveIndices:     []phase0.ValidatorIndex{0, 1},
		EffectiveBalances: []uint32{32, 32},
		AttesterDuties: [][][]duties.ActiveIndiceIndex{
			{{0, 1}},
		},
	}

	aggregationBits := bitfield.NewBitlist(2)
	aggregationBits.SetBitAt(0, true)
	aggregationBits.SetBitAt(1, true)

	activityBitlist := bitfield.NewBitlist(epochStatsValues.ActiveValidators)
	votes := &EpochVotes{}

	voteAmount, slashedVoteAmount, _ := votes.aggregateVotes(
		epochStatsValues, 0, 0, aggregationBits, 0, &activityBitlist, nil, func(phase0.ValidatorIndex) {},
	)

	if slashedVoteAmount != 0 {
		t.Errorf("slashedVoteAmount = %d, want 0 for nil slashed set", slashedVoteAmount)
	}
	if want := phase0.Gwei(64) * EtherGweiFactor; voteAmount != want {
		t.Errorf("voteAmount = %d, want %d", voteAmount, want)
	}
}

// TestEpochStatsPacked_SlashedIndicesRoundTrip verifies the slashed-indice positions survive the
// SSZ pack/unpack cycle used to persist unfinalized epoch duties.
func TestEpochStatsPacked_SlashedIndicesRoundTrip(t *testing.T) {
	es := &EpochStats{
		epoch: 5,
		values: &EpochStatsValues{
			ActiveValidators:  4,
			ActiveIndices:     []phase0.ValidatorIndex{10, 11, 12, 13},
			EffectiveBalances: []uint32{32, 32, 32, 32},
			SlashedIndices:    []duties.ActiveIndiceIndex{1, 3},
		},
	}

	packed, err := es.buildPackedSSZ()
	if err != nil {
		t.Fatalf("buildPackedSSZ: %v", err)
	}

	restored := &EpochStats{epoch: 5}
	values, err := restored.parsePackedSSZ(nil, packed, false)
	if err != nil {
		t.Fatalf("parsePackedSSZ: %v", err)
	}

	if len(values.SlashedIndices) != 2 || values.SlashedIndices[0] != 1 || values.SlashedIndices[1] != 3 {
		t.Errorf("SlashedIndices = %v, want [1 3]", values.SlashedIndices)
	}
}

// A validator's slot-0 vote cannot consume their quorum weight at slot 8.
func TestPaymentVotesRepeatAcrossRounds(t *testing.T) {
	values := &EpochStatsValues{ActiveValidators: 2, ActiveIndices: []phase0.ValidatorIndex{10, 11}, EffectiveBalances: []uint32{32, 32}, AttesterDuties: make([][][]duties.ActiveIndiceIndex, 32)}
	values.AttesterDuties[0] = [][]duties.ActiveIndiceIndex{{0, 1}}
	values.AttesterDuties[8] = [][]duties.ActiveIndiceIndex{{0, 1}}
	bits := bitfield.NewBitlist(2)
	bits.SetBitAt(0, true)
	bits.SetBitAt(1, true)
	att := &all.Attestation{Version: spec.DataVersionGloas, Data: &phase0.AttestationData{}, AggregationBits: offbits.Bitlist(bits), CommitteeBits: make([]byte, 8)}
	att.CommitteeBits.SetBitAt(0, true)
	votes := &EpochVotes{}
	epochActivity := bitfield.NewBitlist(2)
	votes.aggregateVotes(values, 0, 0, bits, 0, &epochActivity, nil, func(phase0.ValidatorIndex) {})
	// The epoch summary has already recorded these identities.
	epochWeight, _, _ := votes.aggregateVotes(values, 8, 0, bits, 0, &epochActivity, nil, func(phase0.ValidatorIndex) {})
	if epochWeight != 0 {
		t.Fatal("test did not reproduce the epoch-wide deduplication condition")
	}
	slot0 := bitfield.NewBitlist(2)
	slot8 := bitfield.NewBitlist(2)
	want := phase0.Gwei(64) * EtherGweiFactor
	if got := votes.aggregatePaymentVotes(values, 0, att, &slot0); got != want {
		t.Fatalf("slot0 weight %d, want %d", got, want)
	}
	if got := votes.aggregatePaymentVotes(values, 8, att, &slot8); got != want {
		t.Fatalf("repeat-round weight %d, want %d", got, want)
	}
	if got := votes.aggregatePaymentVotes(values, 8, att, &slot8); got != 0 {
		t.Fatalf("duplicate aggregate double counted %d", got)
	}
}

// Attestations can name committees that the computed duties do not hold.
func TestAggregateVotes_SkipsUnknownCommittee(t *testing.T) {
	values := &EpochStatsValues{
		ActiveValidators:  2,
		ActiveIndices:     []phase0.ValidatorIndex{0, 1},
		EffectiveBalances: []uint32{32, 32},
		AttesterDuties:    [][][]duties.ActiveIndiceIndex{{{0, 1}}},
	}
	bits := bitfield.NewBitlist(4)
	bits.SetBitAt(2, true)
	activity := bitfield.NewBitlist(2)
	votes := &EpochVotes{}
	for _, tc := range []struct {
		slot      phase0.Slot
		committee uint64
	}{{0, 1}, {0, 2}, {1, 0}} {
		vote, slashed, size := votes.aggregateVotes(
			values, tc.slot, tc.committee, bits, 0, &activity, nil, func(phase0.ValidatorIndex) {},
		)
		if vote != 0 || slashed != 0 || size != 0 {
			t.Errorf("%+v: got %d %d %d, want zeros", tc, vote, slashed, size)
		}
	}
}
