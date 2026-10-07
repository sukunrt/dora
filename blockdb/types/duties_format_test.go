package types

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"reflect"
	"testing"
)

// buildTestEpochDuties constructs an EpochDuties whose committee sizes follow the
// same SplitOffset layout the indexer uses, filling members with sequential
// global indices so round-trips are easy to verify.
func buildTestEpochDuties(validatorCount, slotsPerEpoch, committeesPerSlot, ptcSize uint64) *EpochDuties {
	committeesCount := committeesPerSlot * slotsPerEpoch
	committees := make([][][]uint64, slotsPerEpoch)
	next := uint64(0)
	for slotIndex := range slotsPerEpoch {
		slotCommittees := make([][]uint64, committeesPerSlot)
		for c := range committeesPerSlot {
			indexOffset := slotIndex*committeesPerSlot + c
			start := splitOffset(validatorCount, committeesCount, indexOffset)
			end := splitOffset(validatorCount, committeesCount, indexOffset+1)
			members := make([]uint64, end-start)
			for i := range members {
				members[i] = next
				next++
			}
			slotCommittees[c] = members
		}
		committees[slotIndex] = slotCommittees
	}

	var ptc [][]uint64
	if ptcSize > 0 {
		ptc = make([][]uint64, slotsPerEpoch)
		for slotIndex := range slotsPerEpoch {
			members := make([]uint64, ptcSize)
			for i := range members {
				members[i] = (slotIndex*ptcSize + uint64(i)) % validatorCount
			}
			ptc[slotIndex] = members
		}
	}

	return &EpochDuties{
		FirstSlot:         100 * slotsPerEpoch,
		Epoch:             100,
		ValidatorCount:    validatorCount,
		SlotsPerEpoch:     slotsPerEpoch,
		CommitteesPerSlot: committeesPerSlot,
		PtcSize:           ptcSize,
		Committees:        committees,
		Ptc:               ptc,
	}
}

func TestEpochDutiesRoundTrip(t *testing.T) {
	cases := []struct {
		name                                                      string
		validatorCount, slotsPerEpoch, committeesPerSlot, ptcSize uint64
	}{
		{"small-single-committee", 97, 32, 1, 0},
		{"even-split", 2048, 32, 4, 0},
		{"uneven-split", 1000003, 32, 64, 0},
		{"with-ptc", 500000, 32, 64, 512},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := buildTestEpochDuties(tc.validatorCount, tc.slotsPerEpoch, tc.committeesPerSlot, tc.ptcSize)

			encoded, err := EncodeEpochDuties(src)
			if err != nil {
				t.Fatalf("encode failed: %v", err)
			}

			got, err := DecodeEpochDuties(src.FirstSlot, encoded)
			if err != nil {
				t.Fatalf("decode failed: %v", err)
			}

			if !reflect.DeepEqual(got.Committees, src.Committees) {
				t.Fatalf("committees mismatch after round-trip")
			}
			if !reflect.DeepEqual(got.Ptc, src.Ptc) {
				t.Fatalf("ptc mismatch after round-trip")
			}

			// Per-slot ranged access must match the committees too.
			header, err := DecodeDutiesHeader(encoded)
			if err != nil {
				t.Fatalf("header decode failed: %v", err)
			}
			for slotIndex := range tc.slotsPerEpoch {
				off, length := header.AttesterSlotRange(slotIndex)
				committees, err := header.SplitSlotCommittees(slotIndex, encoded[off:off+length])
				if err != nil {
					t.Fatalf("slot %d split failed: %v", slotIndex, err)
				}
				if !reflect.DeepEqual(committees, src.Committees[slotIndex]) {
					t.Fatalf("slot %d committees mismatch via ranged access", slotIndex)
				}
			}
		})
	}
}

func TestDutiesHeaderVersioning(t *testing.T) {
	base := buildTestEpochDuties(2048, 32, 4, 0)

	// v1: zero dependent root encodes a 40-byte header with version 1.
	v1Header := EncodeDutiesHeader(base)
	if len(v1Header) != DutiesHeaderSize {
		t.Fatalf("v1 header size = %d, want %d", len(v1Header), DutiesHeaderSize)
	}
	h1, err := DecodeDutiesHeader(v1Header)
	if err != nil {
		t.Fatalf("v1 header decode failed: %v", err)
	}
	if h1.Version != 1 {
		t.Fatalf("v1 header version = %d, want 1", h1.Version)
	}
	if h1.Flags&DutiesFlagDiverging != 0 {
		t.Fatalf("v1 header must not set diverging flag")
	}
	if h1.DependentRoot != ([32]byte{}) {
		t.Fatalf("v1 header dependent root = %x, want zero", h1.DependentRoot)
	}

	// v2: non-zero dependent root encodes a 72-byte header carrying the root.
	depRoot := [32]byte{}
	for i := range depRoot {
		depRoot[i] = byte(i + 1)
	}
	diverging := buildTestEpochDuties(2048, 32, 4, 0)
	diverging.DependentRoot = depRoot
	diverging.Diverging = true

	v2Header := EncodeDutiesHeader(diverging)
	if len(v2Header) != DutiesHeaderSizeV2 {
		t.Fatalf("v2 header size = %d, want %d", len(v2Header), DutiesHeaderSizeV2)
	}
	h2, err := DecodeDutiesHeader(v2Header)
	if err != nil {
		t.Fatalf("v2 header decode failed: %v", err)
	}
	if h2.Version != 2 {
		t.Fatalf("v2 header version = %d, want 2", h2.Version)
	}
	if h2.Flags&DutiesFlagDiverging == 0 {
		t.Fatalf("v2 header must set diverging flag")
	}
	if h2.DependentRoot != depRoot {
		t.Fatalf("v2 header dependent root = %x, want %x", h2.DependentRoot, depRoot)
	}
}

func TestEpochDutiesRoundTripDiverging(t *testing.T) {
	depRoot := [32]byte{}
	for i := range depRoot {
		depRoot[i] = byte(0xa0 + i)
	}
	src := buildTestEpochDuties(1000003, 32, 64, 0)
	src.DependentRoot = depRoot

	encoded, err := EncodeEpochDuties(src)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	got, err := DecodeEpochDuties(src.FirstSlot, encoded)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if got.DependentRoot != depRoot {
		t.Fatalf("dependent root mismatch: got %x want %x", got.DependentRoot, depRoot)
	}
	if !reflect.DeepEqual(got.Committees, src.Committees) {
		t.Fatalf("committees mismatch after v2 round-trip")
	}

	// Ranged access must respect the larger v2 header offset.
	header, err := DecodeDutiesHeader(encoded)
	if err != nil {
		t.Fatalf("header decode failed: %v", err)
	}
	for slotIndex := range src.SlotsPerEpoch {
		off, length := header.AttesterSlotRange(slotIndex)
		committees, err := header.SplitSlotCommittees(slotIndex, encoded[off:off+length])
		if err != nil {
			t.Fatalf("slot %d split failed: %v", slotIndex, err)
		}
		if !reflect.DeepEqual(committees, src.Committees[slotIndex]) {
			t.Fatalf("slot %d committees mismatch via ranged access", slotIndex)
		}
	}
}

func TestEpochDutiesProposerRoundTrip(t *testing.T) {
	depRoot := [32]byte{}
	for i := range depRoot {
		depRoot[i] = byte(0x30 + i)
	}
	src := buildTestEpochDuties(500000, 32, 64, 512)
	src.DependentRoot = depRoot
	src.ProposerDuties = make([]uint64, src.SlotsPerEpoch)
	for i := range src.ProposerDuties {
		src.ProposerDuties[i] = uint64(1000 + i)
	}
	// An out-of-range proposer must be stored as the 0 sentinel.
	src.ProposerDuties[3] = uint64(1) << 60

	encoded, err := EncodeEpochDuties(src)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	header, err := DecodeDutiesHeader(encoded)
	if err != nil {
		t.Fatalf("header decode failed: %v", err)
	}
	if header.Version < 2 {
		t.Fatalf("object with proposer duties must be v2, got v%d", header.Version)
	}

	got, err := DecodeEpochDuties(src.FirstSlot, encoded)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if uint64(len(got.ProposerDuties)) != src.SlotsPerEpoch {
		t.Fatalf("proposer duties length = %d, want %d", len(got.ProposerDuties), src.SlotsPerEpoch)
	}
	for i := range got.ProposerDuties {
		want := src.ProposerDuties[i]
		if i == 3 {
			want = 0
		}
		if got.ProposerDuties[i] != want {
			t.Fatalf("proposer[%d] = %d, want %d", i, got.ProposerDuties[i], want)
		}
	}
	// Committees and PTC must still round-trip alongside the proposer section.
	if !reflect.DeepEqual(got.Committees, src.Committees) {
		t.Fatalf("committees mismatch after proposer round-trip")
	}
	if !reflect.DeepEqual(got.Ptc, src.Ptc) {
		t.Fatalf("ptc mismatch after proposer round-trip")
	}
}

func TestSlotCommitteesRoundTrip(t *testing.T) {
	committees := [][]uint64{
		{1, 2, 3},
		{},
		{1000000, 999999, 0, 42},
	}

	blob, err := EncodeSlotCommittees(committees)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	got, err := DecodeSlotCommittees(blob)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if !reflect.DeepEqual(got, committees) {
		t.Fatalf("slot committees mismatch: got %v want %v", got, committees)
	}
}

func TestIndexListRoundTrip(t *testing.T) {
	indices := []uint64{0, 1, 255, 256, 1 << 40, (1 << 48) - 1}

	blob, err := EncodeIndexList(indices)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	got := DecodeIndexList(blob, DutiesIndexWidth)
	if !reflect.DeepEqual(got, indices) {
		t.Fatalf("index list mismatch: got %v want %v", got, indices)
	}
}

// buildRoundDuties uses one complete validator permutation per round, repeated
// across the epoch. Uneven committee lengths follow the per-round split.
func buildRoundDuties(validatorCount, slotsPerEpoch, slotsPerRound, committeesPerSlot, ptcSize uint64) *EpochDuties {
	d := buildTestEpochDuties(validatorCount, slotsPerRound, committeesPerSlot, ptcSize)
	oneRound := d.Committees
	d.SlotsPerEpoch = slotsPerEpoch
	d.CommitteeSlotsPerRound = slotsPerRound
	d.Committees = make([][][]uint64, slotsPerEpoch)
	d.ProposerDuties = make([]uint64, slotsPerEpoch)
	if ptcSize > 0 {
		d.Ptc = make([][]uint64, slotsPerEpoch)
	}
	for slot := range slotsPerEpoch {
		d.Committees[slot] = oneRound[slot%slotsPerRound]
		d.ProposerDuties[slot] = 1000 + slot
		if ptcSize > 0 {
			d.Ptc[slot] = make([]uint64, ptcSize)
			for i := range ptcSize {
				d.Ptc[slot][i] = (slot + i) % validatorCount
			}
		}
	}
	d.DependentRoot = [32]byte{0x42}
	d.Diverging = true
	return d
}

func TestRoundEpochDutiesRoundTripAndRanges(t *testing.T) {
	for _, validatorCount := range []uint64{320, 321} {
		t.Run(fmt.Sprint(validatorCount), func(t *testing.T) {
			src := buildRoundDuties(validatorCount, 32, 8, 2, 7)
			encoded, err := EncodeEpochDuties(src)
			if err != nil {
				t.Fatal(err)
			}
			header, err := DecodeDutiesHeader(encoded[:DutiesHeaderSizeV3])
			if err != nil {
				t.Fatal(err)
			}
			if header.Version != 3 || header.ValidatorCount != validatorCount || header.CommitteeSlotsPerRound != 8 {
				t.Fatalf("wrong round header: %+v", header)
			}
			expectedSize := uint64(DutiesHeaderSizeV3) + (validatorCount*4+32+32*7)*uint64(DutiesIndexWidth)
			if uint64(len(encoded)) != expectedSize {
				t.Fatalf("object size=%d want=%d", len(encoded), expectedSize)
			}
			decoded, err := DecodeEpochDuties(src.FirstSlot, encoded)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decoded, src) {
				t.Fatal("round object did not round trip")
			}
			// These slices model the S3 backend's independent range reads.
			for slot := range src.SlotsPerEpoch {
				off, length := header.AttesterSlotRange(slot)
				committees, err := header.SplitSlotCommittees(slot, encoded[off:off+length])
				if err != nil || !reflect.DeepEqual(committees, src.Committees[slot]) {
					t.Fatalf("slot %d ranged committees mismatch: %v", slot, err)
				}
				off, length = header.PtcSlotRange(slot)
				ptc := DecodeIndexList(encoded[off:off+length], header.IndexWidth)
				if !reflect.DeepEqual(ptc, src.Ptc[slot]) {
					t.Fatalf("slot %d ranged PTC mismatch", slot)
				}
			}
			off, length := header.ProposerSectionRange()
			if !reflect.DeepEqual(DecodeIndexList(encoded[off:off+length], header.IndexWidth), src.ProposerDuties) {
				t.Fatal("ranged proposers mismatch")
			}
			if validatorCount == 321 && len(decoded.Committees[0][0]) != len(decoded.Committees[8][0]) {
				t.Fatal("uneven split must repeat each round")
			}
			for _, cut := range []int{DutiesHeaderSizeV3, len(encoded) - 1} {
				if _, err := DecodeEpochDuties(src.FirstSlot, encoded[:cut]); err == nil {
					t.Fatalf("truncation at %d accepted", cut)
				}
			}
		})
	}
}

func TestStockDutiesV2BytesUnchanged(t *testing.T) {
	fixture, err := hex.DecodeString("445554590002000600000000000000640000000000000004000000020000000100000001000000000100000000000000000000000000000000000000000000000000000000000000000000000000000000000001000000000002000000000003000000000007000000000008000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	d := buildTestEpochDuties(4, 2, 1, 1)
	d.DependentRoot = [32]byte{1}
	d.ProposerDuties = []uint64{7, 8}
	for _, period := range []uint64{0, 2} {
		d.CommitteeSlotsPerRound = period
		encoded, err := EncodeEpochDuties(d)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(encoded, fixture) {
			t.Fatal("standard v2 bytes changed")
		}
	}
	decoded, err := DecodeEpochDuties(d.FirstSlot, fixture)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.CommitteeSlotsPerRound != 0 || !reflect.DeepEqual(decoded.Committees, d.Committees) {
		t.Fatal("legacy v2 semantics changed")
	}
	// Reserved bytes in existing v2 objects must not become a round period.
	binary.BigEndian.PutUint32(fixture[36:40], 7)
	h, err := DecodeDutiesHeader(fixture)
	if err != nil || h.CommitteeSlotsPerRound != 0 {
		t.Fatalf("legacy reserved bytes interpreted as round metadata: %v", err)
	}
}

func TestRoundDutiesRejectMalformedShapes(t *testing.T) {
	for _, mutate := range []func(*EpochDuties){
		func(d *EpochDuties) { d.CommitteeSlotsPerRound = 3 },
		func(d *EpochDuties) { d.CommitteeSlotsPerRound = 33 },
		func(d *EpochDuties) { d.Committees[0] = nil },
		func(d *EpochDuties) { d.Committees[1][0] = append(d.Committees[1][0], 7) },
	} {
		d := buildRoundDuties(321, 32, 8, 2, 0)
		mutate(d)
		if _, err := EncodeEpochDuties(d); err == nil {
			t.Fatal("malformed round duties accepted")
		}
	}
	header := EncodeDutiesHeader(buildRoundDuties(321, 32, 8, 2, 0))
	for _, period := range []uint32{0, 3, 33} {
		bad := bytes.Clone(header)
		binary.BigEndian.PutUint32(bad[36:40], period)
		if _, err := DecodeDutiesHeader(bad); err == nil {
			t.Fatalf("invalid period %d accepted", period)
		}
	}
	bad := bytes.Clone(header)
	binary.BigEndian.PutUint16(bad[4:6], DutiesFormatVersion+1)
	if _, err := DecodeDutiesHeader(bad); err == nil {
		t.Fatal("unknown format version accepted")
	}
	bad = bytes.Clone(header)
	binary.BigEndian.PutUint64(bad[16:24], maxIndexValue)
	binary.BigEndian.PutUint32(bad[24:28], 1<<32-1)
	binary.BigEndian.PutUint32(bad[36:40], 1)
	if _, err := DecodeDutiesHeader(bad); err == nil {
		t.Fatal("overflowing repeated attester section accepted")
	}
}
