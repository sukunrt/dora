package handlers

import (
	"github.com/ethpandaops/dora/dbtypes"
	"github.com/ethpandaops/dora/indexer/beacon/statetransition"
	"github.com/ethpandaops/dora/services"
	"github.com/ethpandaops/dora/types/models"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
)

// resolveSlotBuilderPayment uses source votes for round networks because stored
// quorum fields from older indexers have no derivation version. Both slot detail
// and lists must report unavailable when those source votes have been pruned.
func resolveSlotBuilderPayment(slot *dbtypes.Slot) *models.SlotPageBuilderPayment {
	indexer := services.GlobalBeaconService.GetBeaconIndexer()
	specs := services.GlobalBeaconService.GetChainState().GetSpecs()
	var root phase0.Root
	copy(root[:], slot.Root)
	var weight, base uint64
	var percent float32
	known := false
	if specs.CommitteeSlotsPerRound() != specs.SlotsPerEpoch {
		weight, base, percent, known = indexer.GetLiveBuilderPayment(phase0.Slot(slot.Slot), root)
	} else {
		weight = slot.BuilderPaymentWeight
		percent = slot.BuilderPaymentPercent
		base = indexer.GetBuilderPaymentBase(phase0.Slot(slot.Slot), root)
		if base == 0 && percent > 0 {
			base = uint64(float64(weight) / float64(percent) * 100)
		}
		known = base > 0
	}
	return &models.SlotPageBuilderPayment{BaseKnown: known, Weight: weight, Base: base, Percent: float64(percent), Quorum: statetransition.BuilderPaymentQuorumPercent, MetQuorum: known && float64(percent) >= statetransition.BuilderPaymentQuorumPercent}
}
