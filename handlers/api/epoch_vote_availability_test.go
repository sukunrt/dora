package api

import (
	"encoding/json"
	"testing"
)

func TestEpochAPIsMarkUnavailableVotes(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		for _, tt := range []struct {
			name   string
			data   any
			fields []string
		}{
			{"epoch", &APIEpochResponseV1{Epoch: 2, EpochVotesUnavailable: unavailable, GlobalParticipationRate: 23, VotedEther: 77, ProposedBlocks: 32}, []string{"globalparticipationrate", "votedether"}},
			{"epochs", &APIEpochInfo{Epoch: 2, EpochVotesUnavailable: unavailable, VoteParticipation: 23.12, TargetVoted: 77, HeadVoted: 88, TotalVoted: 99, ProposedBlocks: 32}, []string{"vote_participation", "target_voted", "head_voted", "total_voted"}},
			{"health", &APIEpochHealthResponseV1{Epoch: 2, EpochVotesUnavailable: unavailable, VoteParticipation: 23.12, VotedEther: 77, ProposalParticipation: 100, PayloadParticipation: 99, Healthy: false, ProposedBlocks: 32}, []string{"vote_participation", "voted_ether", "healthy"}},
		} {
			t.Run(tt.name, func(t *testing.T) {
				encoded, err := json.Marshal(tt.data)
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
				if tt.name == "health" && (string(object["proposal_participation"]) != "100" || string(object["payload_participation"]) != "99") {
					t.Error("proposal or payload metric changed")
				}
			})
		}
	}
}

func TestNetworkSplitsMarkUnavailableEpochWeight(t *testing.T) {
	data := &APINetworkSplitInfo{EpochVotesUnavailable: true, ReadyClientCount: 3, TotalChainWeight: 77, LastEpochVotes: []uint64{77}, LastEpochParticipation: []float64{23.12}}
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"total_chain_weight", "last_epoch_votes", "last_epoch_participation"} {
		if string(object[field]) != "null" {
			t.Errorf("%s should be null", field)
		}
	}
	if string(object["ready_client_count"]) != "3" {
		t.Error("client support count changed")
	}
}
