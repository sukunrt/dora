package services

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/dora/clients/consensus"
	"github.com/ethpandaops/dora/db"
)

// roundParticipationKey is the explorer_state key holding the ring buffer.
const roundParticipationKey = "prysm.recent_rounds"

// roundParticipationRingSize is how many finished rounds are kept.
const roundParticipationRingSize = 32

// roundParticipationBackfill is how many past rounds a cold start fetches.
const roundParticipationBackfill = 10

// RoundParticipation is one finished round's FFG stake.
type RoundParticipation struct {
	Round        uint64 `json:"round"`
	VotedGwei    uint64 `json:"voted_gwei"`
	EligibleGwei uint64 `json:"eligible_gwei"`
	Timestamp    int64  `json:"ts"`
}

// RoundParticipationIndexer polls the fork's per-round participation endpoint
// once per round and keeps the last few rounds in explorer_state.
type RoundParticipationIndexer struct {
	ctx           context.Context
	logger        logrus.FieldLogger
	consensusPool *consensus.Pool

	cacheMutex sync.RWMutex
	cache      []*RoundParticipation
}

func NewRoundParticipationIndexer(
	ctx context.Context,
	logger logrus.FieldLogger,
	consensusPool *consensus.Pool,
) *RoundParticipationIndexer {
	return &RoundParticipationIndexer{
		ctx:           ctx,
		logger:        logger,
		consensusPool: consensusPool,
	}
}

func (ri *RoundParticipationIndexer) StartUpdater() {
	go ri.runLoop()
}

func (ri *RoundParticipationIndexer) runLoop() {
	defer func() {
		if err := recover(); err != nil {
			ri.logger.Errorf("round participation indexer panic: %v", err)
		}
	}()

	ri.loadCache()

	for {
		chainState := ri.consensusPool.GetChainState()
		slotsPerRound := chainState.SlotsPerRound()
		if slotsPerRound == 0 {
			// Not the decoupled fork (or specs not loaded yet).
			select {
			case <-ri.ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}

			continue
		}

		if err := ri.update(); err != nil {
			ri.logger.WithError(err).Warn("could not update round participation")
		}

		select {
		case <-ri.ctx.Done():
			return
		case <-time.After(ri.sleepToNextRound(slotsPerRound)):
		}
	}
}

// sleepToNextRound waits until ~90% through the current round, when the round
// before it is complete and its late attestations are included.
func (ri *RoundParticipationIndexer) sleepToNextRound(slotsPerRound uint64) time.Duration {
	chainState := ri.consensusPool.GetChainState()
	specs := chainState.GetSpecs()
	if specs == nil || specs.SlotDurationMs == 0 {
		return 30 * time.Second
	}

	roundDuration := time.Duration(specs.SlotDurationMs*slotsPerRound) * time.Millisecond
	slotInRound := uint64(chainState.CurrentSlot()) % slotsPerRound
	elapsed := time.Duration(specs.SlotDurationMs*slotInRound) * time.Millisecond
	target := roundDuration * 9 / 10

	if elapsed < target {
		return target - elapsed
	}

	return roundDuration - elapsed + target
}

func (ri *RoundParticipationIndexer) update() error {
	chainState := ri.consensusPool.GetChainState()
	currentRound := chainState.CurrentRound()
	if currentRound == 0 {
		return nil
	}

	// Round currentRound-1 has finished; it is the newest one worth asking for.
	target := currentRound - 1

	from := target
	if len(ri.snapshot()) == 0 {
		if target > roundParticipationBackfill {
			from = target - roundParticipationBackfill
		} else {
			from = 0
		}
	} else if newest := ri.newestRound(); newest < target {
		from = newest + 1
	} else {
		return nil
	}

	client := ri.consensusPool.GetReadyEndpoint(consensus.AnyClient)
	if client == nil {
		return nil
	}

	changed := false

	for round := from; round <= target; round++ {
		p, err := client.GetRPCClient().GetPrysmRoundParticipation(ri.ctx, round)
		if err != nil {
			ri.logger.WithError(err).Debugf("could not fetch participation for round %v", round)
			continue
		}

		ri.put(&RoundParticipation{
			Round:        p.Round,
			VotedGwei:    p.VotedGwei,
			EligibleGwei: p.EligibleGwei,
			Timestamp:    time.Now().Unix(),
		})

		changed = true
	}

	if !changed {
		return nil
	}

	return ri.persist()
}

func (ri *RoundParticipationIndexer) loadCache() {
	entries := []*RoundParticipation{}
	if _, err := db.GetExplorerState(ri.ctx, roundParticipationKey, &entries); err != nil {
		return
	}

	ri.cacheMutex.Lock()
	ri.cache = entries
	ri.cacheMutex.Unlock()
}

func (ri *RoundParticipationIndexer) persist() error {
	entries := ri.snapshot()

	return db.RunDBTransaction(func(tx *sqlx.Tx) error {
		return db.SetExplorerState(ri.ctx, tx, roundParticipationKey, entries)
	})
}

func (ri *RoundParticipationIndexer) put(entry *RoundParticipation) {
	ri.cacheMutex.Lock()
	defer ri.cacheMutex.Unlock()

	for i, e := range ri.cache {
		if e.Round == entry.Round {
			ri.cache[i] = entry
			return
		}
	}

	ri.cache = append(ri.cache, entry)
	sort.Slice(ri.cache, func(i, j int) bool { return ri.cache[i].Round < ri.cache[j].Round })

	if len(ri.cache) > roundParticipationRingSize {
		ri.cache = ri.cache[len(ri.cache)-roundParticipationRingSize:]
	}
}

func (ri *RoundParticipationIndexer) newestRound() uint64 {
	ri.cacheMutex.RLock()
	defer ri.cacheMutex.RUnlock()

	if len(ri.cache) == 0 {
		return 0
	}

	return ri.cache[len(ri.cache)-1].Round
}

func (ri *RoundParticipationIndexer) snapshot() []*RoundParticipation {
	ri.cacheMutex.RLock()
	defer ri.cacheMutex.RUnlock()

	out := make([]*RoundParticipation, len(ri.cache))
	copy(out, ri.cache)

	return out
}

// GetRoundParticipation returns the stored participation for a round, or nil.
func (ri *RoundParticipationIndexer) GetRoundParticipation(round uint64) *RoundParticipation {
	ri.cacheMutex.RLock()
	defer ri.cacheMutex.RUnlock()

	for _, e := range ri.cache {
		if e.Round == round {
			return e
		}
	}

	return nil
}
