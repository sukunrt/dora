package models

import (
	"encoding/json"
	"testing"

	dynssz "github.com/pk910/dynamic-ssz"
)

func TestEpochVotesJSONAvailability(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		for _, tt := range []struct {
			name   string
			data   any
			fields []string
		}{
			{"epoch", &EpochPageData{Epoch: 2, EpochVotesUnavailable: unavailable, TargetVoted: 77, HeadVoted: 88, TotalVoted: 99, TargetVoteParticipation: 23.12, HeadVoteParticipation: 97, TotalVoteParticipation: 99, FinalityThreshold: 66.67},
				[]string{"target_voted", "target_voted_slashed", "head_voted", "total_voted", "target_vote_participation", "head_vote_participation", "total_vote_participation", "target_vote_slashed_participation", "target_vote_wrong_participation", "head_vote_wrong_participation", "missing_participation", "finality_threshold"}},
			{"epochs", &EpochsPageDataEpoch{Epoch: 2, EpochVotesUnavailable: unavailable, TargetVoted: 77, HeadVoted: 88, TotalVoted: 99, TargetVoteParticipation: 23.12, HeadVoteParticipation: 97, TotalVoteParticipation: 99},
				[]string{"target_voted", "head_voted", "total_voted", "target_vote_participation", "head_vote_participation", "total_vote_participation"}},
			{"index", &IndexPageDataEpochs{Epoch: 2, EpochVotesUnavailable: unavailable, TargetVoted: 77, VoteParticipation: 23.12, ProposalParticipation: 100, PayloadParticipation: 99}, []string{"voted", "votep"}},
		} {
			t.Run(tt.name, func(t *testing.T) {
				// Exercise the SSZ cache round trip, including the availability flag.
				ssz := dynssz.NewDynSsz(map[string]any{}, dynssz.WithExtendedTypes())
				encoded, err := ssz.MarshalSSZ(tt.data)
				if err != nil {
					t.Fatalf("SSZ marshal: %v", err)
				}
				if err := ssz.UnmarshalSSZ(tt.data, encoded); err != nil {
					t.Fatalf("SSZ unmarshal: %v", err)
				}
				encoded, err = json.Marshal(tt.data)
				if err != nil {
					t.Fatal(err)
				}
				var object map[string]json.RawMessage
				if err := json.Unmarshal(encoded, &object); err != nil {
					t.Fatal(err)
				}
				for _, field := range tt.fields {
					value, ok := object[field]
					if !ok || (string(value) == "null") != unavailable {
						t.Errorf("%s = %s, unavailable = %v", field, value, unavailable)
					}
				}
				if string(object["epoch"]) != "2" {
					t.Error("epoch field changed")
				}
				if tt.name == "index" && (string(object["proposalp"]) != "100" || string(object["payloadp"]) != "99") {
					t.Error("proposal or payload metric changed")
				}
			})
		}
	}
}
