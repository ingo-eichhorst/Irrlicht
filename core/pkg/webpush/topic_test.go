package webpush_test

import (
	"math/rand"
	"regexp"
	"strings"
	"testing"

	"irrlicht/core/pkg/webpush"
)

// topicUUID is the shape the notification policy actually feeds through
// TopicHeader: a 36-character session UUID, four characters over the RFC
// 8030 §5.4 limit. Its mapped value is committed as a golden because
// stability across builds is the point — the push service only collapses
// "newer replaces older" when the same session keeps producing the same
// Topic.
const (
	topicUUID       = "6f2c9a80-1f2b-4e3a-9d4e-8b5a2c7d1e90"
	topicUUIDMapped = "-IR-ApSqaeC701xSYW2aicb4Tk7Z8I0t"
)

var validTopic = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

func TestTopicHeader_ValidKeyPassesThrough(t *testing.T) {
	for _, key := range []string{
		"a",
		"session-42_x",
		strings.Repeat("A", 32), // the exact length ceiling
	} {
		if got := webpush.TopicHeader(key); got != key {
			t.Fatalf("TopicHeader(%q) = %q, want it unchanged", key, got)
		}
	}
}

func TestTopicHeader_UUIDGolden(t *testing.T) {
	got := webpush.TopicHeader(topicUUID)
	if len(got) != 32 {
		t.Fatalf("TopicHeader(uuid) is %d chars, want exactly 32", len(got))
	}
	if got != topicUUIDMapped {
		t.Fatalf("TopicHeader(%q) = %q, want the committed %q — a changed mapping breaks collapse for every in-flight session", topicUUID, got, topicUUIDMapped)
	}
}

// TestTopicHeader_AlwaysValidProperty: whatever goes in, what comes out is a
// legal Topic header value. Axes: length (0, 1, 32, 33, long), alphabet
// (base64url, spaces, punctuation, non-ASCII, NUL), and random bytes.
func TestTopicHeader_AlwaysValidProperty(t *testing.T) {
	inputs := []string{
		"",
		" ",
		"\x00",
		"héllo wörld",
		"a topic with spaces",
		strings.Repeat("x", 33), // one over the limit: valid alphabet, invalid length
		strings.Repeat("x", 500),
		topicUUID,
	}
	rng := rand.New(rand.NewSource(20260814))
	for i := 0; i < 200; i++ {
		b := make([]byte, rng.Intn(64))
		rng.Read(b)
		inputs = append(inputs, string(b))
	}
	for _, in := range inputs {
		got := webpush.TopicHeader(in)
		if !validTopic.MatchString(got) {
			t.Fatalf("TopicHeader(%q) = %q — not a valid RFC 8030 §5.4 Topic", in, got)
		}
		if again := webpush.TopicHeader(in); again != got {
			t.Fatalf("TopicHeader(%q) is not deterministic: %q then %q", in, got, again)
		}
	}
}
