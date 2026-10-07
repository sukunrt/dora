package models

import "encoding/json"

// MarshalEpochVotes keeps unavailable epoch vote metrics explicitly null in JSON.
// Cached page models retain numeric fields because SSZ caches do not preserve nil
// pointers unless the schema explicitly opts in to optional values. Callers must
// pass an alias without MarshalJSON to avoid recursively invoking the method.
func MarshalEpochVotes(data any, unavailable bool, fields ...string) ([]byte, error) {
	encoded, err := json.Marshal(data)
	if err != nil || !unavailable {
		return encoded, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		return nil, err
	}
	for _, field := range fields {
		object[field] = json.RawMessage("null")
	}
	return json.Marshal(object)
}

func (data *EpochPageData) MarshalJSON() ([]byte, error) {
	type plain EpochPageData
	return MarshalEpochVotes((*plain)(data), data.EpochVotesUnavailable,
		"target_voted", "target_voted_slashed", "head_voted", "total_voted",
		"target_vote_participation", "head_vote_participation", "total_vote_participation",
		"target_vote_slashed_participation", "target_vote_wrong_participation",
		"head_vote_wrong_participation", "missing_participation", "finality_threshold")
}

func (data *EpochsPageDataEpoch) MarshalJSON() ([]byte, error) {
	type plain EpochsPageDataEpoch
	return MarshalEpochVotes((*plain)(data), data.EpochVotesUnavailable,
		"target_voted", "head_voted", "total_voted", "target_vote_participation",
		"head_vote_participation", "total_vote_participation")
}

func (data *IndexPageDataEpochs) MarshalJSON() ([]byte, error) {
	type plain IndexPageDataEpochs
	return MarshalEpochVotes((*plain)(data), data.EpochVotesUnavailable, "voted", "votep")
}
