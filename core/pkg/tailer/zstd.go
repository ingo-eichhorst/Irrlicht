package tailer

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/klauspost/compress/zstd"
)

const (
	zstdFrameMagic          = 0xfd2fb528
	zstdSkippableMagicMask  = 0xfffffff0
	zstdSkippableMagicValue = 0x184d2a50

	// A frame must be validated, including its checksum, before its events can
	// change parser state. Buffering one decoded frame makes that validation
	// atomic. The bound is four times the existing per-line safety limit.
	maxDecodedZstdFrameSize = 4 * maxTranscriptLineSize
)

var errPartialZstdFrame = errors.New("zstd frame is incomplete")

func isZstdTranscript(path string) bool {
	return strings.HasSuffix(path, ".zstd")
}

// scanZstdFrames processes complete, independently compressed frames and
// keeps endOffset in compressed-file bytes. An incomplete final frame is a
// live append in progress: it is left at the prior frame boundary for the
// next pass. A complete but invalid frame is returned as an error.
func (t *TranscriptTailer) scanZstdFrames(file *os.File, startPos, fileSize int64) transcriptScanResult {
	res := transcriptScanResult{endOffset: startPos}
	if startPos >= fileSize {
		return res
	}
	decoder, err := zstd.NewReader(nil,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxMemory(uint64(maxDecodedZstdFrameSize)),
	)
	if err != nil {
		res.err = fmt.Errorf("create zstd decoder: %w", err)
		return res
	}
	defer decoder.Close()

	for res.endOffset < fileSize {
		remaining := fileSize - res.endOffset
		frameSize, frameErr := zstdFrameSize(io.NewSectionReader(file, res.endOffset, remaining))
		switch {
		case errors.Is(frameErr, io.EOF), errors.Is(frameErr, errPartialZstdFrame):
			return res
		case frameErr != nil:
			res.err = fmt.Errorf("read zstd frame at compressed offset %d: %w", res.endOffset, frameErr)
			return res
		}

		decoded, decodeErr := decodeZstdFrame(decoder, io.NewSectionReader(file, res.endOffset, frameSize))
		if decodeErr != nil {
			res.err = fmt.Errorf("decode zstd frame at compressed offset %d: %w", res.endOffset, decodeErr)
			return res
		}
		if len(decoded) > 0 && decoded[len(decoded)-1] != '\n' {
			res.err = fmt.Errorf("decode zstd frame at compressed offset %d: complete frame ends mid-line", res.endOffset)
			return res
		}

		frameScan := t.scanNewLinesAfter(
			bufio.NewReaderSize(bytes.NewReader(decoded), 64*1024),
			0,
			res.turnDoneSeen,
		)
		if frameScan.err != nil {
			res.err = frameScan.err
			return res
		}
		if frameScan.endOffset != int64(len(decoded)) {
			res.err = fmt.Errorf("decode zstd frame at compressed offset %d: processed %d of %d decoded bytes", res.endOffset, frameScan.endOffset, len(decoded))
			return res
		}

		mergeZstdFrameScan(&res, frameScan)
		res.endOffset += frameSize
	}
	return res
}

func decodeZstdFrame(decoder *zstd.Decoder, frame io.Reader) ([]byte, error) {
	if err := decoder.Reset(frame); err != nil {
		return nil, err
	}
	decoded, err := io.ReadAll(io.LimitReader(decoder, maxDecodedZstdFrameSize+1))
	if err != nil {
		return nil, err
	}
	if len(decoded) > maxDecodedZstdFrameSize {
		return nil, fmt.Errorf("decoded frame exceeds %d-byte limit", maxDecodedZstdFrameSize)
	}
	return decoded, nil
}

func mergeZstdFrameScan(dst *transcriptScanResult, src transcriptScanResult) {
	dst.linesParsed += src.linesParsed
	dst.substantive = dst.substantive || src.substantive
	dst.sawManualCompact = dst.sawManualCompact || src.sawManualCompact
	dst.sawUserBlockingClosed = dst.sawUserBlockingClosed || src.sawUserBlockingClosed
	dst.sawMidPassTurnBoundary = dst.sawMidPassTurnBoundary || src.sawMidPassTurnBoundary
	dst.turnDoneSeen = src.turnDoneSeen
}

// zstdFrameSize reads one frame envelope without decoding its blocks. It
// returns the compressed byte length of a standard or skippable frame.
func zstdFrameSize(r io.Reader) (int64, error) {
	magic, err := readZstdFrameMagic(r)
	if err != nil {
		return 0, err
	}
	if magic&zstdSkippableMagicMask == zstdSkippableMagicValue {
		return zstdSkippableFrameSize(r)
	}
	if magic != zstdFrameMagic {
		return 0, fmt.Errorf("invalid zstd frame magic %#x", magic)
	}
	return zstdStandardFrameSize(r)
}

func readZstdFrameMagic(r io.Reader) (uint32, error) {
	var magicBytes [4]byte
	n, err := io.ReadFull(r, magicBytes[:])
	if err != nil {
		if n == 0 && errors.Is(err, io.EOF) {
			return 0, io.EOF
		}
		return 0, errPartialZstdFrame
	}
	return binary.LittleEndian.Uint32(magicBytes[:]), nil
}

func zstdSkippableFrameSize(r io.Reader) (int64, error) {
	var payloadSizeBytes [4]byte
	if err := readZstdFrameBytes(r, payloadSizeBytes[:]); err != nil {
		return 0, err
	}
	payloadSize := int64(binary.LittleEndian.Uint32(payloadSizeBytes[:]))
	if err := skipZstdFrameBytes(r, payloadSize); err != nil {
		return 0, err
	}
	return 8 + payloadSize, nil
}

func zstdStandardFrameSize(r io.Reader) (int64, error) {
	var descriptor [1]byte
	if err := readZstdFrameBytes(r, descriptor[:]); err != nil {
		return 0, err
	}
	fhd := descriptor[0]
	if fhd&(1<<3) != 0 {
		return 0, errors.New("zstd frame header has reserved bit set")
	}
	headerRemainder := zstdHeaderRemainderSize(fhd)
	if err := skipZstdFrameBytes(r, headerRemainder); err != nil {
		return 0, err
	}
	blocksSize, err := zstdBlocksSize(r)
	if err != nil {
		return 0, err
	}
	size := int64(5) + headerRemainder + blocksSize
	if fhd&(1<<2) != 0 {
		if err := skipZstdFrameBytes(r, 4); err != nil {
			return 0, err
		}
		size += 4
	}
	return size, nil
}

func zstdHeaderRemainderSize(fhd byte) int64 {
	singleSegment := fhd&(1<<5) != 0
	size := int64(0)
	if !singleSegment {
		size++
	}
	size += []int64{0, 1, 2, 4}[fhd&3]
	contentSizeBytes := []int64{0, 2, 4, 8}[fhd>>6]
	if singleSegment && fhd>>6 == 0 {
		contentSizeBytes = 1
	}
	return size + contentSizeBytes
}

func zstdBlocksSize(r io.Reader) (int64, error) {
	size := int64(0)
	for {
		var blockHeader [3]byte
		if err := readZstdFrameBytes(r, blockHeader[:]); err != nil {
			return 0, err
		}
		size += 3
		header := uint32(blockHeader[0]) | uint32(blockHeader[1])<<8 | uint32(blockHeader[2])<<16
		lastBlock := header&1 != 0
		blockType := (header >> 1) & 3
		blockSize := int64(header >> 3)
		if blockType == 3 {
			return 0, errors.New("zstd frame contains reserved block type")
		}
		payloadSize := blockSize
		if blockType == 1 {
			payloadSize = 1
		}
		if err := skipZstdFrameBytes(r, payloadSize); err != nil {
			return 0, err
		}
		size += payloadSize
		if lastBlock {
			return size, nil
		}
	}
}

func readZstdFrameBytes(r io.Reader, dst []byte) error {
	if _, err := io.ReadFull(r, dst); err != nil {
		return errPartialZstdFrame
	}
	return nil
}

func skipZstdFrameBytes(r io.Reader, n int64) error {
	if n == 0 {
		return nil
	}
	if _, err := io.CopyN(io.Discard, r, n); err != nil {
		return errPartialZstdFrame
	}
	return nil
}
