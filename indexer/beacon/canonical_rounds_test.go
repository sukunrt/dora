package beacon

import (
	"context"
	"sync"
	"testing"

	"github.com/ethpandaops/dora/clients/consensus"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
)

func roundCanonicalFixture() (*Indexer, []*Block) {
	indexer := &Indexer{consensusPool: consensus.NewPool(context.Background(), logrus.New())}
	indexer.blockCache = newBlockCache(indexer)
	indexer.forkCache = newForkCache(indexer)
	indexer.forkCache.finalizedForkId = 1
	blocks := make([]*Block, 5)
	for i, slot := range []phase0.Slot{0, 10, 11, 10, 99} {
		root := phase0.Root{byte(i + 1)}
		block, _ := indexer.blockCache.createOrGetBlock(root, slot)
		block.forkId = ForkKey(i + 1)
		if i > 0 {
			block.parentRoot = &blocks[0].Root
		}
		if i == 2 {
			block.forkId = 2
			block.parentRoot = &blocks[1].Root
		}
		blocks[i] = block
	}
	for _, block := range blocks[1:] {
		indexer.forkCache.forkMap[block.forkId] = &Fork{forkId: block.forkId, parentFork: 1, headBlock: block}
	}
	indexer.blockCache.latestBlock = blocks[4]
	return indexer, blocks
}

func onlineRoundReport(block *Block, priority int) roundHeadReport {
	return roundHeadReport{root: block.Root, priority: priority, status: consensus.ClientStatusOnline}
}

func TestRoundCanonicalFollowsReadyClientHeads(t *testing.T) {
	indexer, blocks := roundCanonicalFixture()
	genesis, a, aNext, b, orphan := blocks[0], blocks[1], blocks[2], blocks[3], blocks[4]
	for _, tt := range []struct {
		name    string
		reports []roundHeadReport
		bad     []phase0.Root
		want    *Block
	}{
		{"unreported newest orphan", []roundHeadReport{onlineRoundReport(a, 0)}, nil, a},
		{"majority beats high priority minority", []roundHeadReport{onlineRoundReport(a, 0), onlineRoundReport(a, 0), onlineRoundReport(b, 100)}, nil, a},
		{"one-block lag supports chain", []roundHeadReport{onlineRoundReport(a, 0), onlineRoundReport(aNext, 0), onlineRoundReport(b, 50)}, nil, aNext},
		{"priority breaks tie", []roundHeadReport{onlineRoundReport(a, 0), onlineRoundReport(b, 10)}, nil, b},
		{"slot breaks tie", []roundHeadReport{onlineRoundReport(aNext, 0), onlineRoundReport(b, 0)}, nil, aNext},
		{"root breaks tie", []roundHeadReport{onlineRoundReport(b, 0), onlineRoundReport(a, 0)}, nil, a},
		{"offline excluded", []roundHeadReport{onlineRoundReport(a, 0), {root: orphan.Root, priority: 100, status: consensus.ClientStatusOffline}}, nil, a},
		{"optimistic excluded", []roundHeadReport{onlineRoundReport(a, 0), {root: orphan.Root, priority: 100, status: consensus.ClientStatusOptimistic}}, nil, a},
		{"unknown root excluded", []roundHeadReport{onlineRoundReport(a, 0), {root: phase0.Root{99}, priority: 100, status: consensus.ClientStatusOnline}}, nil, a},
		{"bad reported root excluded", []roundHeadReport{onlineRoundReport(a, 0), onlineRoundReport(b, 100)}, []phase0.Root{b.Root}, a},
		{"bad ancestor excluded", []roundHeadReport{onlineRoundReport(aNext, 100), onlineRoundReport(b, 0)}, []phase0.Root{a.Root}, b},
		{"no ready head bootstraps genesis", nil, nil, genesis},
	} {
		t.Run(tt.name, func(t *testing.T) {
			indexer.canonicalHead = nil
			indexer.badChainRoots = tt.bad
			indexer.updateRoundCanonicalChain(tt.reports)
			if got := indexer.canonicalHead; got != tt.want {
				t.Fatalf("canonical head = %v, want %v", got, tt.want)
			}
			for _, head := range indexer.cachedChainHeads {
				if !head.EpochVotesUnavailable || head.AggregatedHeadVotes != 0 || len(head.PerEpochVotingPercent) != 0 {
					t.Error("round head must not expose incompatible epoch voting weight")
				}
				for _, root := range tt.bad {
					if indexer.blockCache.isCanonicalBlock(root, head.HeadBlock.Root) {
						t.Error("bad chain remained available as an alternate head")
					}
				}
			}
		})
	}
}

func TestRoundCanonicalUpdatesWithoutNewBlock(t *testing.T) {
	indexer, blocks := roundCanonicalFixture()
	a, b := blocks[1], blocks[3]
	marker := indexer.blockCache.latestBlock
	indexer.canonicalComputation = marker.Root
	indexer.updateRoundCanonicalChain([]roundHeadReport{onlineRoundReport(a, 0)})
	if !indexer.updateRoundCanonicalChain([]roundHeadReport{onlineRoundReport(b, 0)}) || indexer.canonicalHead != b {
		t.Fatal("reported head change did not refresh canonical head")
	}
	if indexer.blockCache.latestBlock != marker {
		t.Fatal("fixture unexpectedly added a block")
	}
	// Losing ready endpoints must retain the last known canonical head instead
	// of promoting the unreported newest cached block or losing startup state.
	indexer.updateRoundCanonicalChain(nil)
	if indexer.canonicalHead != b {
		t.Fatal("no ready clients should preserve the prior canonical head")
	}
	// A retained head on a newly marked bad chain must fall back to a safe anchor.
	indexer.badChainRoots = []phase0.Root{b.Root}
	indexer.updateRoundCanonicalChain(nil)
	if indexer.canonicalHead != blocks[0] {
		t.Fatal("bad retained head was not replaced by genesis anchor")
	}
}

func TestRoundCanonicalRequiresFinalizedAncestry(t *testing.T) {
	indexer, blocks := roundCanonicalFixture()
	anchor, _ := indexer.blockCache.createOrGetBlock(phase0.Root{23}, 23)
	anchor.parentRoot = &blocks[2].Root
	descendant, _ := indexer.blockCache.createOrGetBlock(phase0.Root{24}, 24)
	descendant.parentRoot = &anchor.Root
	for _, tt := range []struct {
		name  string
		block *Block
		want  bool
	}{
		{"genesis stale head", blocks[0], false},
		{"head below finalized slot", blocks[2], false},
		{"incompatible newer head", blocks[4], false},
		{"checkpoint head", anchor, true},
		{"checkpoint descendant", descendant, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := roundHeadMatchesFinality(indexer.blockCache, tt.block, anchor.Root, 23); got != tt.want {
				t.Fatalf("finality match = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRoundCanonicalCollapsesReportedAncestorForks(t *testing.T) {
	indexer, blocks := roundCanonicalFixture()
	parent, child := blocks[1], blocks[2]
	child.forkId = 6
	indexer.forkCache.forkMap[6] = &Fork{forkId: 6, parentFork: parent.forkId, headBlock: child}
	indexer.updateRoundCanonicalChain([]roundHeadReport{onlineRoundReport(parent, 0), onlineRoundReport(child, 0)})
	supported := 0
	for _, head := range indexer.cachedChainHeads {
		if head.ReadyClientCount > 0 {
			supported++
			if head.HeadBlock != child || head.ReadyClientCount != 2 {
				t.Fatal("same-chain endpoints should support the child once")
			}
		}
	}
	if supported != 1 {
		t.Fatalf("reported branches = %d, want 1", supported)
	}
}

func TestRoundCanonicalSnapshotsDuringHeadUpdates(t *testing.T) {
	indexer, blocks := roundCanonicalFixture()
	a, b := blocks[1], blocks[3]
	indexer.canonicalComputation = indexer.blockCache.latestBlock.Root
	indexer.updateRoundCanonicalChain([]roundHeadReport{onlineRoundReport(a, 0)})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for i := 0; i < 100; i++ {
			head := a
			if i%2 == 1 {
				head = b
			}
			indexer.canonicalHeadMutex.Lock()
			indexer.updateRoundCanonicalChain([]roundHeadReport{onlineRoundReport(head, 0)})
			indexer.canonicalHeadMutex.Unlock()
		}
	}()
	go func() {
		defer workers.Done()
		for i := 0; i < 100; i++ {
			if head := indexer.GetCanonicalHead(nil); head != a && head != b {
				t.Error("unexpected canonical snapshot")
			}
			if heads := indexer.GetChainHeads(); len(heads) == 0 || heads[0].ReadyClientCount != 1 {
				t.Error("unexpected chain head snapshot")
			}
		}
	}()
	workers.Wait()
}
