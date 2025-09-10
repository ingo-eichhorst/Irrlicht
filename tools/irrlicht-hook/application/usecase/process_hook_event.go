package usecase

import (
	"fmt"

	"irrlicht/hook/application/services"
	"irrlicht/hook/domain/event"
	"irrlicht/hook/ports/outbound"
)

// ProcessHookEventUseCase represents the use case for processing a single hook event
type ProcessHookEventUseCase struct {
	eventProcessor *services.EventProcessor
	logger         outbound.Logger
	validator      *event.Validator
}

// NewProcessHookEventUseCase creates a new use case instance
func NewProcessHookEventUseCase(
	eventProcessor *services.EventProcessor,
	logger outbound.Logger,
	validator *event.Validator,
) *ProcessHookEventUseCase {
	return &ProcessHookEventUseCase{
		eventProcessor: eventProcessor,
		logger:         logger,
		validator:      validator,
	}
}

// Execute processes a hook event through the complete workflow
func (uc *ProcessHookEventUseCase) Execute(hookEvent *event.HookEvent) error {
	// Validate the event first
	if err := uc.validator.Validate(hookEvent); err != nil {
		uc.logger.LogError(hookEvent.HookEventName, hookEvent.SessionID,
			fmt.Sprintf("Event validation failed: %v", err))
		return fmt.Errorf("event validation failed: %w", err)
	}

	// Process the event using the application service
	if err := uc.eventProcessor.ProcessEvent(hookEvent); err != nil {
		return fmt.Errorf("event processing failed: %w", err)
	}

	return nil
}