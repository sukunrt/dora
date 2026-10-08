package consensus

import (
	"testing"

	"github.com/ethpandaops/dora/utils"
)

func TestParseAdditiveFfgCommitteesPerSubnetPerSlot(t *testing.T) {
	spec := &ChainSpec{}
	if err := spec.ParseAdditive(map[string]any{"SLOTS_PER_EPOCH": uint64(32)}); err != nil {
		t.Fatal(err)
	}
	if spec.FfgCommitteesPerSubnetPerSlot != 0 {
		t.Fatalf("missing key: got %d, want 0", spec.FfgCommitteesPerSubnetPerSlot)
	}

	values := utils.ParseSpecMap(map[string]any{"FFG_COMMITTEES_PER_SUBNET_PER_SLOT": "3"})
	if err := spec.ParseAdditive(values); err != nil {
		t.Fatal(err)
	}
	if spec.FfgCommitteesPerSubnetPerSlot != 3 {
		t.Fatalf("got %d, want 3", spec.FfgCommitteesPerSubnetPerSlot)
	}
}
