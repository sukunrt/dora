package duties

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"reflect"
	"testing"

	"github.com/ethpandaops/dora/clients/consensus"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
)

func committeeTestSpec(round uint64) *consensus.ChainSpec {
	return &consensus.ChainSpec{ChainSpecPreset: consensus.ChainSpecPreset{
		SlotsPerEpoch: 32, SlotsPerRound: round, TargetCommitteeSize: 128, MaxCommitteesPerSlot: 64,
		ShuffleRoundCount: 90, PtcSize: 512, MaxEffectiveBalanceElectra: 2048_000_000_000,
	}, ChainSpecDomainTypes: consensus.ChainSpecDomainTypes{DomainBeaconAttester: phase0.DomainType{1}, DomainPtcAttester: phase0.DomainType{12}}}
}

func TestAttesterDutiesRepeatPerRound(t *testing.T) {
	mix := phase0.Hash32{1}
	state := &BeaconState{RandaoMix: &mix, GetActiveCount: func() uint64 { return 320 }}
	for _, round := range []uint64{0, 32, 8} {
		specs := committeeTestSpec(round)
		duties, err := GetAttesterDuties(specs, state, 0)
		if err != nil {
			t.Fatal(err)
		}
		period := specs.CommitteeSlotsPerRound()
		seen := map[ActiveIndiceIndex]bool{}
		for slot := uint64(0); slot < period; slot++ {
			if len(duties[slot][0]) != int(320/period) {
				t.Fatalf("round %d slot %d committee size = %d", round, slot, len(duties[slot][0]))
			}
			for _, idx := range duties[slot][0] {
				if seen[idx] {
					t.Fatalf("duplicate in round: %d", idx)
				}
				seen[idx] = true
			}
		}
		if len(seen) != 320 {
			t.Fatalf("round %d did not partition active set", round)
		}
		for slot := period; slot < 32; slot++ {
			if !reflect.DeepEqual(duties[slot], duties[slot%period]) {
				t.Fatalf("slot %d did not repeat round committee", slot)
			}
		}
	}
}

func TestPtcSeatsMatchWeightedSelectionReference(t *testing.T) {
	// Exercise the source protocol: epoch-seeded round committee, slot-seeded
	// 16-bit SHA256 rejection sampling, traversing candidates with replacement.
	specs := committeeTestSpec(8)
	mix := phase0.Hash32{1}
	state := &BeaconState{RandaoMix: &mix, GetActiveCount: func() uint64 { return 320 }, GetEffectiveBalance: func(ActiveIndiceIndex) phase0.Gwei { return 32_000_000_000 }}
	attesters, err := GetAttesterDuties(specs, state, 0)
	if err != nil {
		t.Fatal(err)
	}
	seats, err := GetPtcDuties(specs, state, attesters[31], 31)
	if err != nil {
		t.Fatal(err)
	}
	epochSeed := GetSeed(specs, state, 0, specs.DomainPtcAttester)
	var seedInput [40]byte
	copy(seedInput[:32], epochSeed[:])
	binary.LittleEndian.PutUint64(seedInput[32:], 31)
	seed := sha256.Sum256(seedInput[:])
	candidates := attesters[31][0]
	want := make([]ActiveIndiceIndex, 0, 512)
	for i := uint64(0); len(want) < 512; i++ {
		var randomInput [40]byte
		copy(randomInput[:32], seed[:])
		binary.LittleEndian.PutUint64(randomInput[32:], i/16)
		random := sha256.Sum256(randomInput[:])
		offset := (i % 16) * 2
		if 32_000_000_000*uint64(65535) >= specs.MaxEffectiveBalanceElectra*uint64(binary.LittleEndian.Uint16(random[offset:offset+2])) {
			want = append(want, candidates[i%uint64(len(candidates))])
		}
	}
	if !reflect.DeepEqual(seats, want) {
		t.Fatal("PTC seat order differs from protocol reference")
	}
	unique := map[ActiveIndiceIndex]bool{}
	h := sha256.New()
	var buf [8]byte
	for _, idx := range seats {
		unique[idx] = true
		binary.LittleEndian.PutUint64(buf[:], uint64(idx))
		h.Write(buf[:])
	}
	if len(seats) != 512 || len(unique) != 40 {
		t.Fatalf("got %d seats, %d validators", len(seats), len(unique))
	}
	const expected = "43de6f61275a03f90a0870b8d4e327828c68ef65698665fb452a50e34087de9c"
	if got := hex.EncodeToString(h.Sum(nil)); got != expected {
		t.Fatalf("PTC fingerprint %s, want %s", got, expected)
	}
}
