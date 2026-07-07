package antigravity

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver for database/sql
)

// Antigravity keeps token usage and the canonical model id in a per-conversation
// SQLite store that sits beside the transcript — the transcript.jsonl itself
// carries only the human-readable model display name and no usage at all. For a
// transcript at
//
//	<root>/brain/<conv-id>/.system_generated/logs/transcript.jsonl
//
// the store is <root>/conversations/<conv-id>.db. Reading it lights up the
// context bar (#719); without it every Antigravity session reports zero tokens,
// so ComputeContextUtilization returns pressure "unknown" and both UIs hide the
// bar. A missing or unreadable store degrades to no-usage — the pre-#719 state.

// dbModelTokens is one read of the conversation store: the canonical model id
// and the latest generation's context-token count. Either may be zero/empty
// when the store's shape is unrecognized; the caller then emits no usage.
type dbModelTokens struct {
	model         string
	contextTokens int64
}

// dbCache memoizes one store read keyed by the store's (mtime, size) plus the
// -wal file size, so a backfill of a long transcript — every historical
// turn_done re-reading the same growing .db — costs one query per change, not
// one per turn (mirrors kirocli's sidecarCache). The -wal size is part of the
// key because live agy writes land in the WAL before any checkpoint; without it
// an active session's bar would freeze on the first read (see readStoreModelTokens).
type dbCache struct {
	mtime   time.Time
	size    int64
	walSize int64
	val     dbModelTokens
	ok      bool
}

// storePathForTranscript maps a transcript path to its sibling conversation
// store, or "" when the path is not a recognized Antigravity transcript. The
// <conv-id> directory is shared between the brain transcript and the
// conversations store, so it is the join key.
func storePathForTranscript(transcriptPath string) string {
	conv := sessionIDFromPath(transcriptPath)
	if conv == "" {
		return ""
	}
	// Climb to <root>: transcript.jsonl → logs → .system_generated → <conv-id>
	// → brain → <root>.
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(transcriptPath)))))
	return filepath.Join(root, "conversations", conv+".db")
}

// readStoreModelTokens opens the conversation store read-only and returns the
// canonical model id plus the latest generation's context-token count, memoized
// by (mtime, size). The bool is false when the store is missing, locked, or its
// gen_metadata shape is unrecognized — callers degrade to no-usage.
func readStoreModelTokens(transcriptPath string, cache *dbCache) (dbModelTokens, bool) {
	dbPath := storePathForTranscript(transcriptPath)
	if dbPath == "" {
		return dbModelTokens{}, false
	}
	fi, err := os.Stat(dbPath)
	if err != nil {
		return dbModelTokens{}, false
	}
	// Live agy writes land in the -wal file; the main .db's mtime/size only move
	// on checkpoint, so the WAL must be part of the freshness key or an active
	// session's bar would freeze until SQLite next checkpoints. Use its size, not
	// mtime, so our own read-only opens (which may touch -wal/-shm) don't
	// spuriously invalidate the cache and re-trigger an O(N) backfill.
	var walSize int64
	if wfi, werr := os.Stat(dbPath + "-wal"); werr == nil {
		walSize = wfi.Size()
	}
	if cache != nil && cache.ok &&
		fi.ModTime().Equal(cache.mtime) && fi.Size() == cache.size && walSize == cache.walSize {
		return cache.val, true
	}

	// Read-only, WAL-aware, short timeout so a live agy write never blocks the
	// daemon (mirrors the opencode metrics reader).
	db, err := sql.Open("sqlite", dbPath+"?mode=ro&_journal=WAL&_timeout=500")
	if err != nil {
		return dbModelTokens{}, false
	}
	defer db.Close()

	// gen_metadata holds one protobuf blob per generation; the highest idx is
	// the most recent, whose prompt-token count is the current context fill.
	var blob []byte
	if err := db.QueryRow(`SELECT data FROM gen_metadata ORDER BY idx DESC LIMIT 1`).Scan(&blob); err != nil {
		return dbModelTokens{}, false
	}

	val := decodeGenMetadata(blob)
	if cache != nil {
		*cache = dbCache{mtime: fi.ModTime(), size: fi.Size(), walSize: walSize, val: val, ok: true}
	}
	return val, true
}

// gen_metadata protobuf field numbers, reverse-engineered from agy's store (no
// .proto ships with Antigravity). Empirically stable across the 0.5.x CLI:
//
//	msg(top) #1              → the generation's usage submessage
//	  usage  #4              → token-count block
//	    block #5  varint     → prompt tokens = current context-window occupancy
//	  usage  #19 string      → canonical model id ("gemini-3.1-pro-low")
//
// Field #21 carries the human display name ("Gemini 3.1 Pro (Low)"); #19 is
// preferred because only the dash-form id resolves in the capacity context-
// window map.
const (
	fieldUsage          = 1
	fieldTokenBlock     = 4
	fieldPromptTokens   = 5
	fieldCanonicalModel = 19
)

// decodeGenMetadata extracts the canonical model id and context-token count
// from one gen_metadata protobuf blob. Any field absent leaves its zero value,
// so an unrecognized shape yields an empty result rather than an error.
func decodeGenMetadata(blob []byte) dbModelTokens {
	usage := protoBytesField(blob, fieldUsage)
	if usage == nil {
		return dbModelTokens{}
	}
	var out dbModelTokens
	if model := protoBytesField(usage, fieldCanonicalModel); model != nil {
		out.model = string(model)
	}
	if block := protoBytesField(usage, fieldTokenBlock); block != nil {
		out.contextTokens = protoVarintField(block, fieldPromptTokens)
	}
	return out
}

// protoBytesField returns the value of the first length-delimited field with
// the given number, or nil. protoVarintField returns the first varint field's
// value, or 0. Hand-rolled because agy ships no .proto and we need only a few
// fields — a dependency-free walker beats pulling in protobuf reflection.
func protoBytesField(buf []byte, field int) []byte {
	var out []byte
	walkProto(buf, func(f, wire int, data []byte, _ uint64) bool {
		if f == field && wire == 2 {
			out = data
			return true
		}
		return false
	})
	return out
}

func protoVarintField(buf []byte, field int) int64 {
	var out int64
	walkProto(buf, func(f, wire int, _ []byte, num uint64) bool {
		if f == field && wire == 0 {
			out = int64(num)
			return true
		}
		return false
	})
	return out
}

// walkProto iterates a protobuf message's top-level fields, calling fn with each
// field's number, wire type, and payload (LEN bytes in data; varint value in
// num). It stops when fn returns true and returns silently on truncated or
// malformed input, so a missing field reads as "no data".
func walkProto(buf []byte, fn func(field, wire int, data []byte, num uint64) bool) {
	for i := 0; i < len(buf); {
		tag, n := readVarint(buf[i:])
		if n == 0 {
			return
		}
		i += n
		field, wire := int(tag>>3), int(tag&7)

		next, stop, ok := consumeProtoField(buf, i, field, wire, fn)
		if !ok {
			return
		}
		i = next
		if stop {
			return
		}
	}
}

// consumeProtoField consumes the payload of one field per its wire type,
// invoking fn for varint (0) and length-delimited (2) fields. It returns the
// buffer offset just past the field, whether fn asked to stop the walk, and
// whether the payload was well-formed — ok is false on truncated or
// malformed input, which walkProto treats as "stop with no data".
func consumeProtoField(buf []byte, i, field, wire int, fn func(field, wire int, data []byte, num uint64) bool) (next int, stop bool, ok bool) {
	switch wire {
	case 0: // varint
		v, n := readVarint(buf[i:])
		if n == 0 {
			return 0, false, false
		}
		return i + n, fn(field, wire, nil, v), true
	case 2: // length-delimited
		l, n := readVarint(buf[i:])
		if n == 0 {
			return 0, false, false
		}
		i += n
		if i+int(l) > len(buf) {
			return 0, false, false
		}
		return i + int(l), fn(field, wire, buf[i:i+int(l)], 0), true
	case 1: // 64-bit
		if i+8 > len(buf) {
			return 0, false, false
		}
		return i + 8, false, true
	case 5: // 32-bit
		if i+4 > len(buf) {
			return 0, false, false
		}
		return i + 4, false, true
	default:
		return 0, false, false
	}
}

// readVarint decodes a base-128 varint, returning the value and bytes consumed
// (0, 0 on truncation or overflow).
func readVarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b); i++ {
		v |= uint64(b[i]&0x7f) << (7 * uint(i))
		if b[i]&0x80 == 0 {
			return v, i + 1
		}
		if i >= 9 {
			return 0, 0 // overflow
		}
	}
	return 0, 0 // truncated
}
