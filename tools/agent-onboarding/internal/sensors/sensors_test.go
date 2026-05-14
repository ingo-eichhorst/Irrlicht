package sensors

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestWriteSignals_emitsJSONLOnePerSignal(t *testing.T) {
	var buf bytes.Buffer
	ch := make(chan Signal, 3)
	ch <- Signal{Ts: time.Unix(1, 0).UTC(), Sensor: "test", Kind: "hello", Payload: json.RawMessage(`{"x":1}`)}
	ch <- Signal{Ts: time.Unix(2, 0).UTC(), Sensor: "test", Kind: "world"}
	close(ch)

	if err := WriteSignals(context.Background(), &buf, ch); err != nil {
		t.Fatalf("WriteSignals: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), buf.String())
	}
	var s Signal
	if err := json.Unmarshal([]byte(lines[0]), &s); err != nil {
		t.Fatalf("unmarshal line 0: %v", err)
	}
	if s.Kind != "hello" || s.Sensor != "test" {
		t.Errorf("line 0 wrong: %+v", s)
	}
}

func TestWriteSignals_honorsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan Signal) // never sends
	cancel()
	err := WriteSignals(ctx, &bytes.Buffer{}, ch)
	if err == nil {
		t.Fatal("want ctx.Err, got nil")
	}
}

func TestMerge_closesWhenAllInputsClose(t *testing.T) {
	a := make(chan Signal, 1)
	b := make(chan Signal, 1)
	a <- Signal{Sensor: "a"}
	b <- Signal{Sensor: "b"}
	close(a)
	close(b)

	out := Merge(a, b)
	var seen []string
	for s := range out {
		seen = append(seen, s.Sensor)
	}
	sort.Strings(seen)
	if got := strings.Join(seen, ","); got != "a,b" {
		t.Errorf("want a,b; got %s", got)
	}
}

func TestMarshalPayload_nilPassthrough(t *testing.T) {
	b, err := MarshalPayload(nil)
	if err != nil {
		t.Fatal(err)
	}
	if b != nil {
		t.Errorf("want nil, got %s", string(b))
	}
}

func TestMarshalPayload_struct(t *testing.T) {
	b, err := MarshalPayload(struct {
		A int `json:"a"`
	}{A: 7})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"a":7}` {
		t.Errorf("got %s", string(b))
	}
}
