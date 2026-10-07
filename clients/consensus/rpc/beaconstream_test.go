package rpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethpandaops/dora/clients/consensus/rpc/eventstream"
	"github.com/sirupsen/logrus"
)

// bidEventPayload is a SignedExecutionPayloadBid container as sent by Nimbus
// (captured from a glamsterdam-devnet-7 node), which omits the versioned
// envelope other clients wrap the event in.
const bidEventPayload = `{"message":{"parent_block_hash":"0xd82482d1aef3ee998e6a638ddc8ee7f7540aa121af3a1e89f65fed989f56e449","parent_block_root":"0xf9b85fb511fa7a8561fac828fc390e61a64804e851da4816d7a62cc4648d3e2e","block_hash":"0xd18d394228734ff07fd17542df9294c86b779999805e7895379b9f6699b68487","prev_randao":"0xf66c5be439378224329f796401778ab62f725192604ba4a4a5be5b4514d04817","fee_recipient":"0xf97e180c050e5ab072211ad2c213eb5aee4df134","gas_limit":"300000000","builder_index":"3","slot":"168775","value":"471708347","execution_payment":"0","blob_kzg_commitments":["0xa95caabd009e189b9f205e0328ff847ad886e4f8e719bd7219875fbb9688fb3fbe7704bb1dfa7e2993a3dea8d0cf767d"],"execution_requests_root":"0x87b69a306c8e430d0857f7c4ac5e27cecffa1108d43c2e5df7388056fea7a423"},"signature":"0xb399a174693747e9437ba1d94be3ad3e7556a388a6d3e4e7589cea7310735e04296086a957f9a3652d5e14731561151900f4532f72227b60f06449068d610794e8ee273bd00b3179030242096fb95a17cf0277528c7c6c8ddf5a408fca069ad2"}`

func TestParseExecutionPayloadBidEvent(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{
			name: "versioned envelope",
			data: fmt.Sprintf(`{"version":"gloas","data":%s}`, bidEventPayload),
		},
		{
			name: "bare container (nimbus)",
			data: bidEventPayload,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bid, err := parseExecutionPayloadBidEvent([]byte(tt.data))
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			if bid.Message == nil {
				t.Fatal("parsed bid has no message")
			}
			if uint64(bid.Message.Slot) != 168775 {
				t.Errorf("unexpected slot: %d", bid.Message.Slot)
			}
			if uint64(bid.Message.BuilderIndex) != 3 {
				t.Errorf("unexpected builder index: %d", bid.Message.BuilderIndex)
			}
			if uint64(bid.Message.Value) != 471708347 {
				t.Errorf("unexpected value: %d", bid.Message.Value)
			}
		})
	}
}

func TestIsUnsupportedTopicError(t *testing.T) {
	tests := []struct {
		name    string
		code    int
		message string
		events  uint16
		want    bool
	}{
		{"prysm inclusion list", 400, `{"message":"inclusion_list: invalid topic name","code":400}`, StreamInclusionListEvent, true},
		{"plain fast confirmation", 400, `unsupported topic: fast_confirmation`, StreamFastConfirmationEvent, true},
		{"unprocessable topic", 422, `Unknown topic 'inclusion_list'`, StreamInclusionListEvent, true},
		{"unrelated bad request", 400, `{"message":"invalid request"}`, StreamInclusionListEvent, false},
		{"other topic", 400, `fast_confirmation: invalid topic name`, StreamInclusionListEvent, false},
		{"lookalike topic", 400, `inclusion_list_extra: invalid topic name`, StreamInclusionListEvent, false},
		{"missing topic name", 400, `invalid topic name`, StreamInclusionListEvent, false},
		{"temporary topic failure", 400, `inclusion_list is temporarily unavailable`, StreamInclusionListEvent, false},
		{"unauthorized", 401, `inclusion_list: invalid topic name`, StreamInclusionListEvent, false},
		{"forbidden", 403, `inclusion_list: invalid topic name`, StreamInclusionListEvent, false},
		{"missing endpoint", 404, `inclusion_list: invalid topic name`, StreamInclusionListEvent, false},
		{"rate limited", 429, `inclusion_list: invalid topic name`, StreamInclusionListEvent, false},
		{"server failure", 500, `inclusion_list: invalid topic name`, StreamInclusionListEvent, false},
		{"required payload", 400, `execution_payload_available: invalid topic name`, StreamExecutionPayloadEvent, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := fmt.Errorf("subscription failed: %w", eventstream.SubscriptionError{Code: tt.code, Message: tt.message})
			if got := isUnsupportedTopicError(err, tt.events); got != tt.want {
				t.Fatalf("isUnsupportedTopicError = %v, want %v", got, tt.want)
			}
		})
	}
	if isUnsupportedTopicError(errors.New("inclusion_list: invalid topic name"), StreamInclusionListEvent) {
		t.Fatal("a non-HTTP error must not disable a topic")
	}
}

func newTestBeaconStream(t *testing.T, endpoint string) *BeaconStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return &BeaconStream{
		ctx: ctx, ctxCancel: cancel, logger: logger,
		client:    &BeaconClient{endpoint: endpoint},
		ReadyChan: make(chan *BeaconStreamStatus, 10),
		EventChan: make(chan *BeaconStreamEvent, 10),
	}
}

func TestOptionalStreamStopsOnUnsupportedTopic(t *testing.T) {
	for _, tt := range []struct {
		topic string
		event uint16
	}{
		{"inclusion_list", StreamInclusionListEvent},
		{"fast_confirmation", StreamFastConfirmationEvent},
	} {
		t.Run(tt.topic, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if got := r.URL.Query().Get("topics"); got != tt.topic {
					t.Errorf("requested topics = %s, want %s", got, tt.topic)
				}
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `{"message":"%s: invalid topic name","code":400}`, tt.topic)
			}))
			t.Cleanup(server.Close)
			bs := newTestBeaconStream(t, server.URL)
			done := make(chan struct{})
			go func() {
				bs.runAncillaryStream(tt.event)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("unsupported optional stream kept retrying")
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("subscription attempts = %d, want 1", got)
			}
		})
	}
}

func TestOptionalStreamRetriesTemporarySubscriptionFailure(t *testing.T) {
	for _, tt := range []struct {
		topic   string
		event   uint16
		code    int
		message string
	}{
		{"inclusion_list", StreamInclusionListEvent, 503, `{"message":"temporarily unavailable"}`},
		{"fast_confirmation", StreamFastConfirmationEvent, 400, `{"message":"temporary backend failure"}`},
	} {
		t.Run(tt.topic, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					w.WriteHeader(tt.code)
					io.WriteString(w, tt.message)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			t.Cleanup(server.Close)
			bs := newTestBeaconStream(t, server.URL)
			result := make(chan *eventstream.Stream, 1)
			go func() {
				result <- bs.subscribeStream(server.URL, tt.event, true)
			}()
			select {
			case stream := <-result:
				if stream == nil {
					t.Fatal("temporary error disabled optional stream")
				}
				select {
				case <-stream.Ready:
				case <-time.After(2 * time.Second):
					t.Fatal("retried stream did not become ready")
				}
				bs.Close()
				stream.Close()
			case <-time.After(13 * time.Second):
				t.Fatal("temporary error was not retried")
			}
			if got := requests.Load(); got != 2 {
				t.Fatalf("subscription attempts = %d, want 2", got)
			}
		})
	}
}

func TestOptionalStreamStopsOnUnsupportedReconnect(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, ": connected\n\n")
			w.(http.Flusher).Flush()
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"message":"inclusion_list: invalid topic name","code":400}`)
	}))
	t.Cleanup(server.Close)
	bs := newTestBeaconStream(t, server.URL)
	done := make(chan struct{})
	go func() {
		bs.runAncillaryStream(StreamInclusionListEvent)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("unsupported optional reconnect kept retrying")
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("subscription attempts = %d, want 2", got)
	}
}

func TestUnsupportedInclusionListPreservesCoreStreams(t *testing.T) {
	var inclusionRequests atomic.Int32
	emit := make(chan struct{})
	root := "0x" + strings.Repeat("00", 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		topics := r.URL.Query().Get("topics")
		if topics == "inclusion_list" {
			if inclusionRequests.Add(1) == 1 {
				close(emit)
			}
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"message":"inclusion_list: invalid topic name","code":400}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		select {
		case <-emit:
		case <-r.Context().Done():
			return
		}
		switch topics {
		case "block,head":
			fmt.Fprintf(w, "event: block\ndata: {\"slot\":\"1\",\"block\":%q}\n\n", root)
			fmt.Fprintf(w, "event: head\ndata: {\"slot\":\"1\",\"block\":%q,\"state\":%q}\n\n", root, root)
		case "execution_payload_available,execution_payload_bid":
			fmt.Fprintf(w, "event: execution_payload_available\ndata: {\"slot\":\"1\",\"block_root\":%q}\n\n", root)
			fmt.Fprintf(w, "event: execution_payload_bid\ndata: %s\n\n", bidEventPayload)
		default:
			t.Errorf("unexpected requested topics: %s", topics)
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	bs := newTestBeaconStream(t, server.URL)
	bs.events = StreamBlockEvent | StreamHeadEvent | StreamExecutionPayloadEvent | StreamExecutionPayloadBidEvent | StreamInclusionListEvent
	go bs.startStream()
	seen := uint16(0)
	want := bs.events &^ StreamInclusionListEvent
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for seen != want {
		select {
		case event := <-bs.EventChan:
			seen |= event.Event
		case <-timer.C:
			t.Fatalf("events after topic rejection = 0x%x, want 0x%x", seen, want)
		}
	}
	// Wait beyond the subscription retry interval, including additive updates
	// that must not restart a topic already found to be unsupported.
	bs.UpdateEvents(StreamInclusionListEvent)
	select {
	case <-time.After(11 * time.Second):
	case <-bs.ctx.Done():
		t.Fatal("main stream closed after optional topic rejection")
	}
	if got := inclusionRequests.Load(); got != 1 {
		t.Fatalf("inclusion list attempts = %d, want 1", got)
	}
}
