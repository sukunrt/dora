package consensus

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethpandaops/dora/clients/consensus/rpc"
	v1 "github.com/ethpandaops/go-eth2-client/api/v1"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
)

func testFinality(justifiedEpoch, finalizedEpoch phase0.Epoch, justifiedRoot, finalizedRoot byte) *v1.Finality {
	return &v1.Finality{
		PreviousJustified: &phase0.Checkpoint{},
		Justified:         &phase0.Checkpoint{Epoch: justifiedEpoch, Root: phase0.Root{justifiedRoot}},
		Finalized:         &phase0.Checkpoint{Epoch: finalizedEpoch, Root: phase0.Root{finalizedRoot}},
	}
}

func TestFinalizedSlotBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name          string
		slotsPerRound uint64
		finality      *v1.Finality
		rounds        *rpc.FinalityRounds
		boundary      phase0.Slot
		finalized     []phase0.Slot
		unfinalized   []phase0.Slot
	}{
		{"round three targets slot 23", 8, testFinality(0, 0, 4, 3), &rpc.FinalityRounds{Justified: 4, Finalized: 3}, 23, []phase0.Slot{0, 15, 23}, []phase0.Slot{24, 31, 32}},
		{"standard checkpoint targets epoch start", 0, testFinality(6, 5, 6, 5), nil, 160, []phase0.Slot{0, 159, 160}, []phase0.Slot{161, 191, 192}},
		{"zero round does not finalize future slots", 8, testFinality(0, 0, 0, 0), &rpc.FinalityRounds{}, 0, nil, []phase0.Slot{0, 1, 31}},
		{"standard genesis stub", 0, testFinality(0, 0, 0, 0), nil, 0, nil, []phase0.Slot{0, 1, 31}},
		{"round metadata not received", 8, nil, nil, 0, nil, []phase0.Slot{0, 23, 31}},
		{"single slot round checkpoint at genesis", 1, testFinality(0, 0, 1, 1), &rpc.FinalityRounds{Justified: 1, Finalized: 1}, 0, []phase0.Slot{0}, []phase0.Slot{1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := newChainState()
			cs.specs = &ChainSpec{ChainSpecPreset: ChainSpecPreset{SlotsPerEpoch: 32, SlotsPerRound: tc.slotsPerRound}}
			cs.setFinalizedCheckpoint(tc.finality, tc.rounds)
			if got := cs.GetFinalizedSlot(); got != tc.boundary {
				t.Fatalf("boundary=%d, want %d", got, tc.boundary)
			}
			for _, slot := range tc.finalized {
				if !cs.IsSlotFinalized(slot) {
					t.Errorf("slot %d should be finalized", slot)
				}
			}
			for _, slot := range tc.unfinalized {
				if cs.IsSlotFinalized(slot) {
					t.Errorf("slot %d should not be finalized", slot)
				}
			}
		})
	}
}

func TestEpochFinalityRoundBoundaryAndStockCompatibility(t *testing.T) {
	cs := newChainState()
	cs.specs = &ChainSpec{ChainSpecPreset: ChainSpecPreset{SlotsPerEpoch: 32, SlotsPerRound: 8}}
	cs.setFinalizedCheckpoint(testFinality(0, 0, 4, 3), &rpc.FinalityRounds{Justified: 4, Finalized: 3})
	if cs.IsEpochFinalized(0) {
		t.Fatal("round 3 only finalizes through slot 23, not epoch zero")
	}
	cs.setFinalizedCheckpoint(testFinality(1, 0, 5, 4), &rpc.FinalityRounds{Justified: 5, Finalized: 4})
	if !cs.IsEpochFinalized(0) || cs.IsEpochFinalized(1) {
		t.Fatal("round 4 should finalize only epoch zero")
	}
	cs = newChainState()
	cs.specs = &ChainSpec{ChainSpecPreset: ChainSpecPreset{SlotsPerEpoch: 32}}
	cs.setFinalizedCheckpoint(testFinality(6, 5, 6, 5), nil)
	if !cs.IsEpochFinalized(4) || !cs.IsEpochFinalized(5) || cs.IsEpochFinalized(6) {
		t.Fatal("standard epoch 5 checkpoint must retain epoch badges through epoch 5")
	}
	cs = newChainState()
	cs.specs = &ChainSpec{ChainSpecPreset: ChainSpecPreset{SlotsPerEpoch: 32}}
	cs.setFinalizedCheckpoint(testFinality(0, 0, 0, 0), nil)
	if cs.IsEpochFinalized(0) {
		t.Fatal("standard genesis stub must not mark epoch zero finalized")
	}
}

func TestSameEpochCheckpointRootsAdvanceWithRounds(t *testing.T) {
	cs := newChainState()
	cs.specs = &ChainSpec{ChainSpecPreset: ChainSpecPreset{SlotsPerEpoch: 32, SlotsPerRound: 8}}
	cs.setFinalizedCheckpoint(testFinality(0, 0, 2, 1), &rpc.FinalityRounds{Justified: 2, Finalized: 1})
	sub := cs.checkpointDispatcher.Subscribe(3, false)
	defer sub.Unsubscribe()
	cs.setFinalizedCheckpoint(testFinality(0, 0, 4, 3), &rpc.FinalityRounds{Justified: 4, Finalized: 3})
	_, justifiedRoot := cs.GetJustifiedCheckpoint()
	_, finalizedRoot := cs.GetFinalizedCheckpoint()
	if justifiedRoot != (phase0.Root{4}) || finalizedRoot != (phase0.Root{3}) {
		t.Fatal("same epoch checkpoint roots did not advance")
	}
	// A lagging client must not roll either checkpoint back. Its newer justified
	// checkpoint can still be accepted independently of the older finalized one.
	cs.setFinalizedCheckpoint(testFinality(1, 0, 5, 2), &rpc.FinalityRounds{Justified: 5, Finalized: 2})
	_, justifiedRoot = cs.GetJustifiedCheckpoint()
	_, finalizedRoot = cs.GetFinalizedCheckpoint()
	justified, finalized, ok := cs.GetFinalityRounds()
	if !ok || justified != 5 || finalized != 3 || justifiedRoot != (phase0.Root{5}) || finalizedRoot != (phase0.Root{3}) {
		t.Fatal("checkpoint roots and rounds became inconsistent")
	}
	// Only the second update advanced an epoch, preserving the indexer's schedule.
	if len(sub.Channel()) != 1 {
		t.Fatalf("expected one epoch notification, got %d", len(sub.Channel()))
	}
}

func TestRoundFinalityRefreshReservation(t *testing.T) {
	cs := newChainState()
	cs.specs = &ChainSpec{ChainSpecPreset: ChainSpecPreset{SlotsPerEpoch: 32, SlotsPerRound: 8}}
	client := &Client{pool: &Pool{chainState: cs}, headSlot: 23, lastFinalityUpdateSlot: 16}
	if client.reserveFinalityUpdate(24) {
		t.Fatal("wallclock crossed boundary before head: should wait for processed head")
	}
	client.headSlot = 24
	var reserved atomic.Int32
	var workers sync.WaitGroup
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if client.reserveFinalityUpdate(24) {
				reserved.Add(1)
			}
		}()
	}
	workers.Wait()
	if reserved.Load() != 1 {
		t.Fatalf("got %d reservations, want one", reserved.Load())
	}
	// A failed RPC clears the pending flag but does not move the successful round.
	client.finalityUpdatePending = false
	if client.reserveFinalityUpdate(24) {
		t.Fatal("must not repeatedly request on duplicate events in the same slot")
	}
	client.headSlot = 25
	if !client.reserveFinalityUpdate(25) {
		t.Fatal("failed refresh should retry on the next head slot")
	}
	client.finalityUpdatePending = false
	client.lastFinalityUpdateSlot = 25
	client.headSlot = 26
	if client.reserveFinalityUpdate(26) {
		t.Fatal("successful refresh should suppress requests for the rest of the round")
	}
	client.headSlot = 32
	if !client.reserveFinalityUpdate(32) {
		t.Fatal("next head round should refresh immediately")
	}
}

func TestMetadataRefreshCannotSuppressFinality(t *testing.T) {
	cs := newChainState()
	cs.specs = &ChainSpec{ChainSpecPreset: ChainSpecPreset{SlotsPerEpoch: 32}}
	now := time.Now()
	client := &Client{pool: &Pool{chainState: cs}, lastMetadataUpdateTime: now}
	if !client.reserveMetadataUpdate(33, now) {
		t.Fatal("metadata should refresh at epoch slot one")
	}
	if client.lastFinalityUpdateEpoch != 0 {
		t.Fatal("metadata changed the finality schedule")
	}
	if client.reserveFinalityUpdate(33) {
		t.Fatal("standard finality refresh should still wait until epoch slot two")
	}
	if !client.reserveFinalityUpdate(34) {
		t.Fatal("metadata refresh suppressed the checkpoint refresh")
	}
	if client.reserveMetadataUpdate(34, now) {
		t.Fatal("metadata should refresh only once per epoch")
	}
}

func newFinalityTestClient(t *testing.T, handler http.HandlerFunc, rounds bool) (*Client, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	logger := logrus.New().WithField("test", "finality")
	rpcClient, err := rpc.NewBeaconClient("test", server.URL, nil, nil, false, logger)
	if err != nil {
		t.Fatal(err)
	}
	cs := newChainState()
	cs.specs = &ChainSpec{ChainSpecPreset: ChainSpecPreset{SlotsPerEpoch: 32}, ChainSpecConfig: ChainSpecConfig{SlotDurationMs: 12000}}
	if rounds {
		cs.specs.SlotsPerRound = 8
	}
	cs.genesis = &v1.Genesis{GenesisTime: time.Now().Add(-24 * 12 * time.Second)}
	return &Client{pool: &Pool{chainState: cs}, rpcClient: rpcClient, logger: logger, clientCtx: context.Background(), headSlot: 24}, server.Close
}

func writeFinalityResponse(w http.ResponseWriter, justifiedRoot, finalizedRoot byte, justifiedRound, finalizedRound string) {
	fmt.Fprintf(w, `{"data":{"previous_justified":{"epoch":"0","root":"%s"},"current_justified":{"epoch":"0","root":"%s"%s},"finalized":{"epoch":"0","root":"%s"%s}}}`,
		phase0.Root{}.String(), (phase0.Root{justifiedRoot}).String(), justifiedRound, (phase0.Root{finalizedRoot}).String(), finalizedRound)
}

func TestUpdateFinalityRetainsFinalizationWithUnchangedJustifiedRoot(t *testing.T) {
	var calls atomic.Int32
	client, closeServer := newFinalityTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		finalized := byte(calls.Add(1))
		writeFinalityResponse(w, 2, finalized, `,"round":"2"`, fmt.Sprintf(`,"round":"%d"`, finalized))
	}, true)
	defer closeServer()
	for i := 0; i < 2; i++ {
		if _, err := client.updateFinalityCheckpoints(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	_, root, _, _ := client.GetFinalityCheckpoint()
	_, globalRoot := client.pool.chainState.GetFinalizedCheckpoint()
	if root != (phase0.Root{2}) || globalRoot != root {
		t.Fatal("finalization update lost because justified root was unchanged")
	}
	if client.pool.chainState.GetFinalizedSlot() != 15 {
		t.Fatal("raw round was not retained")
	}
}

func TestUpdateFinalityStockResponseHasNoRounds(t *testing.T) {
	client, closeServer := newFinalityTestClient(t, func(w http.ResponseWriter, r *http.Request) { writeFinalityResponse(w, 0, 0, "", "") }, false)
	defer closeServer()
	if _, err := client.updateFinalityCheckpoints(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := client.pool.chainState.GetFinalityRounds(); ok {
		t.Fatal("stock response must not advertise round checkpoints")
	}
}

func TestConcurrentFinalityRequestsAreSerialized(t *testing.T) {
	var active, maximum atomic.Int32
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	client, closeServer := newFinalityTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		for previous := maximum.Load(); n > previous && !maximum.CompareAndSwap(previous, n); previous = maximum.Load() {
		}
		started <- struct{}{}
		<-release
		writeFinalityResponse(w, 2, 1, `,"round":"2"`, `,"round":"1"`)
		active.Add(-1)
	}, true)
	defer closeServer()
	errors := make(chan error, 2)
	go func() { _, err := client.updateFinalityCheckpoints(context.Background()); errors <- err }()
	<-started
	secondAttempted := make(chan struct{})
	go func() {
		close(secondAttempted)
		_, err := client.updateFinalityCheckpoints(context.Background())
		errors <- err
	}()
	<-secondAttempted
	// Hold the first RPC open while the second caller attempts its request.
	// Without serialization both requests reach the server concurrently.
	select {
	case <-started:
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	if maximum.Load() != 1 {
		t.Fatalf("%d overlapping checkpoint requests", maximum.Load())
	}
}

func TestFailedFinalityRefreshRetriesNextHeadSlot(t *testing.T) {
	client, closeServer := newFinalityTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}, true)
	defer closeServer()
	client.lastFinalityUpdateSlot = 16
	if !client.reserveFinalityUpdate(24) {
		t.Fatal("round boundary should request checkpoints")
	}
	if _, err := client.updateFinalityCheckpoints(context.Background()); err == nil {
		t.Fatal("failed RPC unexpectedly succeeded")
	}
	client.finalityUpdatePending = false
	if client.lastFinalityUpdateSlot != 16 {
		t.Fatal("failed RPC marked round refresh successful")
	}
	client.headSlot = 25
	if !client.reserveFinalityUpdate(25) {
		t.Fatal("failed RPC cannot retry next head slot")
	}
}
