package relay

import (
	"encoding/json"
	"testing"
)

type fakeControl struct {
	inputs     []string
	interrupts int
}

func (f *fakeControl) SendInput(id string, d []byte) error {
	f.inputs = append(f.inputs, id+"="+string(d))
	return nil
}
func (f *fakeControl) Interrupt(id string) error { f.interrupts++; return nil }

func ctlFrame(c Control) []byte {
	c.Type = MsgControl
	b, _ := json.Marshal(c)
	return b
}

func TestForwarderHandleControl_GateAndDispatch(t *testing.T) {
	fc := &fakeControl{}
	on := false
	f := &Forwarder{control: fc, controlEnabled: func() bool { return on }}

	// Relay-control toggle off → frame ignored even though a handler exists.
	f.handleControl(ctlFrame(Control{SessionID: "s1", Action: ControlActionInput, Data: "hi"}))
	if len(fc.inputs) != 0 {
		t.Fatalf("toggle off must not dispatch, got %v", fc.inputs)
	}

	on = true
	f.handleControl(ctlFrame(Control{SessionID: "s1", Action: ControlActionInput, Data: "hi"}))
	if len(fc.inputs) != 1 || fc.inputs[0] != "s1=hi" {
		t.Fatalf("input not dispatched: %v", fc.inputs)
	}

	f.handleControl(ctlFrame(Control{SessionID: "s1", Action: ControlActionInterrupt}))
	if fc.interrupts != 1 {
		t.Fatalf("interrupt not dispatched: %d", fc.interrupts)
	}

	// Empty session id and unknown action are ignored.
	f.handleControl(ctlFrame(Control{Action: ControlActionInput, Data: "x"}))
	f.handleControl(ctlFrame(Control{SessionID: "s1", Action: "bogus"}))
	if len(fc.inputs) != 1 || fc.interrupts != 1 {
		t.Fatalf("malformed frames must be ignored: inputs=%v interrupts=%d", fc.inputs, fc.interrupts)
	}

	// A forwarder with no control wiring must not panic.
	(&Forwarder{}).handleControl(ctlFrame(Control{SessionID: "s", Action: ControlActionInput}))
}
