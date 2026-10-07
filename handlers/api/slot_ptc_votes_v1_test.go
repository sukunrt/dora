package api

import (
	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"testing"
)

func TestPtcVotesSeatAndValidatorCounts(t *testing.T) {
	seats := make([]phase0.ValidatorIndex, 512)
	for i := range seats {
		seats[i] = phase0.ValidatorIndex(i%40 + 100)
	}
	full := &gloas.PayloadAttestation{AggregationBits: make([]byte, 64), Data: &gloas.PayloadAttestationData{PayloadPresent: true}}
	for i := range full.AggregationBits {
		full.AggregationBits[i] = 255
	}
	data := &APISlotPtcVotesData{}
	populatePtcVotes(data, []*gloas.PayloadAttestation{full}, seats, 512, func(uint64) string { return "" })
	if data.TotalPtcSize != 512 || data.VoteCount != 512 || data.Participation != 1 || data.Aggregates[0].VotePercent != 100 || data.NonVoterCount != 0 {
		t.Fatalf("invalid full-seat tally: %+v", data)
	}
	if !data.DutiesAvailable || data.UniqueValidatorCount != 40 || data.UniqueVoterCount != 40 || len(data.Aggregates[0].Validators) != 40 {
		t.Fatalf("invalid unique-validator tally: %+v", data)
	}
}

func TestPtcVotesPartialSeatsAndOverlappingAggregates(t *testing.T) {
	// Validator 10 owns seats 0 and 2. A vote in only one of those seats leaves
	// one absent seat, but does not make that validator a non-voter.
	seats := []phase0.ValidatorIndex{10, 11, 10, 12, 13, 14, 15, 16}
	first := &gloas.PayloadAttestation{AggregationBits: []byte{3}, Data: &gloas.PayloadAttestationData{}}
	second := &gloas.PayloadAttestation{AggregationBits: []byte{1}, Data: &gloas.PayloadAttestationData{PayloadPresent: true}}
	data := &APISlotPtcVotesData{}
	populatePtcVotes(data, []*gloas.PayloadAttestation{first, second}, seats, 8, func(uint64) string { return "" })
	if data.Participation != 0.25 || data.NonVoterCount != 6 || data.NonVoterPercent != 75 || data.Aggregates[0].VotePercent != 25 {
		t.Fatalf("invalid seat tally: %+v", data)
	}
	if data.UniqueValidatorCount != 7 || data.UniqueVoterCount != 2 || data.UniqueNonVoterCount != 5 || len(data.NonVoters) != 5 {
		t.Fatalf("invalid unique tally: %+v", data)
	}
	for _, v := range data.NonVoters {
		if v.Index == 10 || v.Index == 11 {
			t.Fatalf("voter reported absent: %d", v.Index)
		}
	}
}

func TestPtcVotesWithoutCompleteDuties(t *testing.T) {
	att := &gloas.PayloadAttestation{AggregationBits: []byte{255}, Data: &gloas.PayloadAttestationData{}}
	data := &APISlotPtcVotesData{}
	populatePtcVotes(data, []*gloas.PayloadAttestation{att}, []phase0.ValidatorIndex{1}, 8, func(uint64) string { return "" })
	if data.DutiesAvailable || len(data.Aggregates[0].Validators) != 0 || data.Participation != 1 || data.Aggregates[0].VotePercent != 100 {
		t.Fatalf("incomplete duties produced incorrect identities/counts: %+v", data)
	}
}

func TestPtcVotesEmptyAggregates(t *testing.T) {
	data := &APISlotPtcVotesData{}
	populatePtcVotes(data, nil, []phase0.ValidatorIndex{10, 11, 10, 12, 13, 14, 15, 16}, 8, func(uint64) string { return "" })
	if data.TotalPtcSize != 8 || data.NonVoterCount != 8 || data.Participation != 0 || data.UniqueNonVoterCount != 7 || !data.DutiesAvailable {
		t.Fatalf("empty votes did not account for absent seats: %+v", data)
	}
}
