package beacon

import (
	"bytes"
	"slices"

	"github.com/ethpandaops/dora/clients/consensus"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
)

type roundHeadReport struct {
	root     phase0.Root
	priority int
	status   consensus.ClientStatus
}

// computeRoundCanonicalChain follows node fork choice on round-based networks.
// Epoch target totals do not describe their round votes. Refresh the small client
// snapshot on every lookup: a reorg can change reported heads without adding a
// block, and endpoint readiness can change without an SSE event.
// The caller holds canonicalHeadMutex; ready-client lookup does not use canonical
// head selection, so this path cannot recursively acquire that mutex.
func (indexer *Indexer) computeRoundCanonicalChain() bool {
	reports := make([]roundHeadReport, 0)
	for _, client := range indexer.GetReadyClients(false) {
		_, root := client.client.GetLastHead()
		reports = append(reports, roundHeadReport{root, client.priority, client.client.GetStatus()})
	}
	return indexer.updateRoundCanonicalChain(reports)
}

func (indexer *Indexer) updateRoundCanonicalChain(reports []roundHeadReport) bool {
	chainState := indexer.consensusPool.GetChainState()
	_, finalizedRoot := chainState.GetFinalizedCheckpoint()
	finalizedSlot := chainState.GetFinalizedSlot()
	valid := func(block *Block) bool {
		if block == nil || block.isDisposed || indexer.blockCache.getBlockByRoot(block.Root) != block {
			return false
		}
		if !roundHeadMatchesFinality(indexer.blockCache, block, finalizedRoot, finalizedSlot) {
			return false
		}
		for _, badRoot := range indexer.badChainRoots {
			if indexer.blockCache.isCanonicalBlock(badRoot, block.Root) {
				return false
			}
		}
		return true
	}
	type candidate struct {
		block    *Block
		clients  int
		priority int
	}
	candidates := make([]candidate, 0)
	seen := make(map[phase0.Root]bool)
	for _, report := range reports {
		block := indexer.blockCache.getBlockByRoot(report.root)
		if report.status != consensus.ClientStatusOnline || !valid(block) || seen[report.root] {
			continue
		}
		seen[report.root] = true
		entry := candidate{block: block, priority: report.priority}
		for _, supporter := range reports {
			if supporter.status != consensus.ClientStatusOnline || !valid(indexer.blockCache.getBlockByRoot(supporter.root)) {
				continue
			}
			// Match the client-fork view's readiness convention: an endpoint
			// at the reported head or one parent behind supports this chain.
			if inChain, distance := indexer.blockCache.getCanonicalDistance(supporter.root, block.Root, 1); inChain && distance < 2 {
				entry.clients++
				if supporter.priority > entry.priority {
					entry.priority = supporter.priority
				}
			}
		}
		candidates = append(candidates, entry)
	}
	slices.SortFunc(candidates, func(a, b candidate) int {
		if a.clients != b.clients {
			return b.clients - a.clients
		}
		if a.priority != b.priority {
			if a.priority > b.priority {
				return -1
			}
			return 1
		}
		if a.block.Slot != b.block.Slot {
			if a.block.Slot > b.block.Slot {
				return -1
			}
			return 1
		}
		return bytes.Compare(a.block.Root[:], b.block.Root[:])
	})

	previous := indexer.canonicalHead
	head := previous
	if len(candidates) > 0 {
		head = candidates[0].block
	} else if !valid(head) {
		// With no usable node head, bootstrap only from the finalized anchor
		// or genesis. An unreported newest block may belong to an orphan.
		_, root := indexer.consensusPool.GetChainState().GetFinalizedCheckpoint()
		head = indexer.blockCache.getBlockByRoot(root)
		if !valid(head) {
			head = nil
			for _, genesis := range indexer.blockCache.getBlocksBySlot(0) {
				if valid(genesis) && (head == nil || bytes.Compare(genesis.Root[:], head.Root[:]) < 0) {
					head = genesis
				}
			}
		}
	}
	if !valid(head) {
		head = nil
	}

	chainHeads := make([]*ChainHead, 0)
	seenForks := make(map[ForkKey]bool)
	addHead := func(block *Block, clients int) {
		if !valid(block) || seenForks[block.forkId] {
			return
		}
		seenForks[block.forkId] = true
		chainHeads = append(chainHeads, &ChainHead{HeadBlock: block, ReadyClientCount: clients, EpochVotesUnavailable: true})
	}
	for _, candidate := range candidates {
		// Different fork IDs can lie on the same observed chain. Report that
		// chain once, retaining its highest-ranked reported head.
		matchingChain := false
		for _, existing := range chainHeads {
			if indexer.blockCache.isCanonicalBlock(candidate.block.Root, existing.HeadBlock.Root) || indexer.blockCache.isCanonicalBlock(existing.HeadBlock.Root, candidate.block.Root) {
				matchingChain = true
				break
			}
		}
		if !matchingChain {
			addHead(candidate.block, candidate.clients)
		}
	}
	addHead(head, 0)
	// Keep other known forks available to explicit fork overrides without
	// assigning their unreported tips fabricated consensus voting weight.
	forks := indexer.forkCache.getForkHeads()
	slices.SortFunc(forks, func(a, b *ForkHead) int {
		if a.ForkId < b.ForkId {
			return -1
		}
		if a.ForkId > b.ForkId {
			return 1
		}
		return 0
	})
	for _, fork := range forks {
		addHead(fork.Block, 0)
	}
	indexer.canonicalHead = head
	indexer.cachedChainHeads = chainHeads
	return head != previous
}

// A client's head and checkpoint snapshots can be observed at different times.
// Independently enforce the pool checkpoint before using a reported head.
func roundHeadMatchesFinality(cache *blockCache, block *Block, root phase0.Root, slot phase0.Slot) bool {
	if root == consensus.NullRoot {
		return true
	}
	if cache.getBlockByRoot(root) != nil {
		return cache.isCanonicalBlock(root, block.Root)
	}
	// When the anchor has already been pruned, cached parent links can still
	// prove ancestry. Otherwise avoid regressing below its known slot boundary.
	return cache.isCanonicalBlock(root, block.Root) || block.Slot >= slot
}
