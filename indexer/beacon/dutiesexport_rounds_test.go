package beacon

import (
	"reflect"
	"testing"

	btypes "github.com/ethpandaops/dora/blockdb/types"
	"github.com/ethpandaops/dora/clients/consensus"
	"github.com/ethpandaops/dora/indexer/beacon/duties"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
)

func TestBuildEpochDutiesRoundPersistence(t *testing.T) {
	specs := &consensus.ChainSpec{ChainSpecPreset: consensus.ChainSpecPreset{
		SlotsPerEpoch: 32, SlotsPerRound: 8, TargetCommitteeSize: 128,
		MaxCommitteesPerSlot: 64, ShuffleRoundCount: 90,
	}}
	state := &duties.BeaconState{
		RandaoMix:      &phase0.Hash32{},
		GetActiveCount: func() uint64 { return 321 },
	}
	committees, err := duties.GetAttesterDuties(specs, state, 3)
	if err != nil {
		t.Fatal(err)
	}
	values := &EpochStatsValues{ActiveValidators: 321, AttesterDuties: committees, ActiveIndices: make([]phase0.ValidatorIndex, 321)}
	for i := range values.ActiveIndices {
		values.ActiveIndices[i] = phase0.ValidatorIndex(1000 + i)
	}
	d := BuildEpochDuties(specs, 3, values)
	if d.CommitteeSlotsPerRound != 8 || d.ValidatorCount != 321 {
		t.Fatalf("wrong exported dimensions: %+v", d)
	}
	encoded, err := btypes.EncodeEpochDuties(d)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := btypes.DecodeEpochDuties(d.FirstSlot, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Committees, d.Committees) {
		t.Fatal("exported shuffled round duties changed after persistence")
	}
	for slot, cs := range decoded.Committees {
		for committee, indices := range cs {
			for member, index := range indices {
				want := uint64(values.ActiveIndices[committees[slot][committee][member]])
				if index != want {
					t.Fatal("persisted duties lost global validator mapping")
				}
			}
		}
	}
}
