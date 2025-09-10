package stdin

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"irrlicht/hook/domain/event"
	"irrlicht/hook/ports/inbound"
)

// StdinReceiver implements the EventReceiver interface for reading events from stdin
type StdinReceiver struct {
	reader      io.Reader
	scanner     *bufio.Scanner
	isRunning   bool
	stopChan    chan struct{}
	mu          sync.Mutex
	maxLineSize int
}

// NewStdinReceiver creates a new stdin event receiver
func NewStdinReceiver() *StdinReceiver {
	return &StdinReceiver{
		reader:      os.Stdin,
		stopChan:    make(chan struct{}),
		maxLineSize: 1024 * 1024, // 1MB max line size
	}
}

// NewStdinReceiverWithReader creates a new stdin receiver with a custom reader (for testing)
func NewStdinReceiverWithReader(reader io.Reader) *StdinReceiver {
	return &StdinReceiver{
		reader:      reader,
		stopChan:    make(chan struct{}),
		maxLineSize: 1024 * 1024,
	}
}

// ReceiveEvents starts receiving events from stdin and sends them to the provided channel
func (sr *StdinReceiver) ReceiveEvents(events chan<- *event.HookEvent, errors chan<- error) error {
	sr.mu.Lock()
	if sr.isRunning {
		sr.mu.Unlock()
		return fmt.Errorf("receiver is already running")
	}
	sr.isRunning = true
	sr.scanner = bufio.NewScanner(sr.reader)
	
	// Set buffer size for large payloads
	buf := make([]byte, 0, 64*1024)
	sr.scanner.Buffer(buf, sr.maxLineSize)
	sr.mu.Unlock()

	go sr.readLoop(events, errors)
	return nil
}

// Stop stops the event receiver
func (sr *StdinReceiver) Stop() error {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	
	if !sr.isRunning {
		return nil
	}
	
	close(sr.stopChan)
	sr.isRunning = false
	return nil
}

// readLoop reads lines from stdin and parses them as events
func (sr *StdinReceiver) readLoop(events chan<- *event.HookEvent, errors chan<- error) {
	defer func() {
		sr.mu.Lock()
		sr.isRunning = false
		sr.mu.Unlock()
	}()

	for {
		select {
		case <-sr.stopChan:
			return
		default:
			if !sr.scanner.Scan() {
				// Check for scanner error
				if err := sr.scanner.Err(); err != nil {
					select {
					case errors <- fmt.Errorf("scanner error: %w", err):
					case <-sr.stopChan:
					}
				}
				// EOF or error, exit the loop
				return
			}

			line := sr.scanner.Text()
			if line == "" {
				continue // Skip empty lines
			}

			// Parse the line as a JSON event
			hookEvent, err := sr.parseEvent(line)
			if err != nil {
				select {
				case errors <- fmt.Errorf("failed to parse event: %w", err):
				case <-sr.stopChan:
					return
				}
				continue
			}

			// Send the event
			select {
			case events <- hookEvent:
			case <-sr.stopChan:
				return
			}
		}
	}
}

// parseEvent parses a JSON line into a HookEvent
func (sr *StdinReceiver) parseEvent(line string) (*event.HookEvent, error) {
	var hookEvent event.HookEvent
	
	if err := json.Unmarshal([]byte(line), &hookEvent); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	
	// Set timestamp if not provided
	if hookEvent.Timestamp == "" {
		hookEvent.Timestamp = time.Now().Format(time.RFC3339)
	}
	
	// Initialize data map if nil
	if hookEvent.Data == nil {
		hookEvent.Data = make(map[string]interface{})
	}
	
	return &hookEvent, nil
}

// IsRunning returns true if the receiver is currently running
func (sr *StdinReceiver) IsRunning() bool {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.isRunning
}

// SetMaxLineSize sets the maximum line size for the scanner
func (sr *StdinReceiver) SetMaxLineSize(size int) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.maxLineSize = size
}

// BatchStdinReceiver receives events in batches for better performance
type BatchStdinReceiver struct {
	*StdinReceiver
	batchSize    int
	batchTimeout time.Duration
}

// NewBatchStdinReceiver creates a new batch stdin receiver
func NewBatchStdinReceiver(batchSize int, batchTimeout time.Duration) *BatchStdinReceiver {
	return &BatchStdinReceiver{
		StdinReceiver: NewStdinReceiver(),
		batchSize:     batchSize,
		batchTimeout:  batchTimeout,
	}
}

// ReceiveEventsBatch receives events in batches
func (bsr *BatchStdinReceiver) ReceiveEventsBatch(batches chan<- []*event.HookEvent, errors chan<- error) error {
	events := make(chan *event.HookEvent, bsr.batchSize*2)
	
	// Start the underlying receiver
	if err := bsr.StdinReceiver.ReceiveEvents(events, errors); err != nil {
		return err
	}
	
	go bsr.batchLoop(events, batches)
	return nil
}

// batchLoop groups events into batches
func (bsr *BatchStdinReceiver) batchLoop(events <-chan *event.HookEvent, batches chan<- []*event.HookEvent) {
	var batch []*event.HookEvent
	ticker := time.NewTicker(bsr.batchTimeout)
	defer ticker.Stop()

	for {
		select {
		case event, ok := <-events:
			if !ok {
				// Channel closed, send final batch if any
				if len(batch) > 0 {
					select {
					case batches <- batch:
					case <-bsr.stopChan:
					}
				}
				return
			}
			
			batch = append(batch, event)
			
			// Send batch if it's full
			if len(batch) >= bsr.batchSize {
				select {
				case batches <- batch:
					batch = batch[:0] // Reset batch
				case <-bsr.stopChan:
					return
				}
			}
			
		case <-ticker.C:
			// Send batch on timeout if it has events
			if len(batch) > 0 {
				select {
				case batches <- batch:
					batch = batch[:0] // Reset batch
				case <-bsr.stopChan:
					return
				}
			}
			
		case <-bsr.stopChan:
			return
		}
	}
}

// StdinEventHandler combines receiving and handling events from stdin
type StdinEventHandler struct {
	receiver inbound.EventReceiver
	handler  inbound.EventHandler
	events   chan *event.HookEvent
	errors   chan error
	stopChan chan struct{}
	mu       sync.Mutex
	isRunning bool
}

// NewStdinEventHandler creates a new stdin event handler
func NewStdinEventHandler(handler inbound.EventHandler) *StdinEventHandler {
	return &StdinEventHandler{
		receiver: NewStdinReceiver(),
		handler:  handler,
		events:   make(chan *event.HookEvent, 100),
		errors:   make(chan error, 10),
		stopChan: make(chan struct{}),
	}
}

// Start starts receiving and handling events
func (seh *StdinEventHandler) Start() error {
	seh.mu.Lock()
	if seh.isRunning {
		seh.mu.Unlock()
		return fmt.Errorf("handler is already running")
	}
	seh.isRunning = true
	seh.mu.Unlock()
	
	// Start receiving events
	if err := seh.receiver.ReceiveEvents(seh.events, seh.errors); err != nil {
		return err
	}
	
	// Start handling events
	go seh.eventLoop()
	go seh.errorLoop()
	
	return nil
}

// Stop stops the event handler
func (seh *StdinEventHandler) Stop() error {
	seh.mu.Lock()
	defer seh.mu.Unlock()
	
	if !seh.isRunning {
		return nil
	}
	
	close(seh.stopChan)
	seh.isRunning = false
	
	// Stop the receiver
	if err := seh.receiver.Stop(); err != nil {
		return err
	}
	
	// Stop the handler
	if err := seh.handler.Stop(); err != nil {
		return err
	}
	
	return nil
}

// eventLoop processes incoming events
func (seh *StdinEventHandler) eventLoop() {
	for {
		select {
		case event := <-seh.events:
			if err := seh.handler.HandleEvent(event); err != nil {
				// Log error but continue processing
				select {
				case seh.errors <- fmt.Errorf("event handling error: %w", err):
				case <-seh.stopChan:
					return
				}
			}
		case <-seh.stopChan:
			return
		}
	}
}

// errorLoop handles errors
func (seh *StdinEventHandler) errorLoop() {
	for {
		select {
		case err := <-seh.errors:
			// In a real implementation, this would log to the configured logger
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		case <-seh.stopChan:
			return
		}
	}
}