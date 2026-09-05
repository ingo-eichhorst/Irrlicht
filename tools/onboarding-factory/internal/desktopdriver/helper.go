package desktopdriver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

const helperProtocolVersion = 1

type helperStatus struct {
	BundleIdentifier         string `json:"bundleIdentifier"`
	DesktopVersion           string `json:"desktopVersion"`
	BundledClaudeCodeVersion string `json:"bundledClaudeCodeVersion"`
}

type helperElement struct {
	Path        []int    `json:"path"`
	Role        string   `json:"role"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Hierarchy   []string `json:"hierarchy"`
	Enabled     *bool    `json:"enabled"`
}

type helperSelector struct {
	Role        string   `json:"role,omitempty"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Hierarchy   []string `json:"hierarchy,omitempty"`
}

type helperPostcondition struct {
	Selector            helperSelector `json:"selector"`
	Condition           string         `json:"condition"`
	TimeoutMilliseconds int            `json:"timeoutMilliseconds"`
}

type helperRequest struct {
	ProtocolVersion int                  `json:"protocolVersion"`
	Command         string               `json:"command"`
	Selector        *helperSelector      `json:"selector,omitempty"`
	Value           *string              `json:"value,omitempty"`
	Postcondition   *helperPostcondition `json:"postcondition,omitempty"`
	Limits          map[string]int       `json:"limits,omitempty"`
	Probes          []helperProbe        `json:"probes,omitempty"`
}

type helperProbe struct {
	Name             string           `json:"name"`
	Selectors        []helperSelector `json:"selectors"`
	Required         bool             `json:"required"`
	RequiresGeometry bool             `json:"requiresGeometry"`
}

type helperResponse struct {
	OK       bool            `json:"ok"`
	Status   helperStatus    `json:"status"`
	Elements []helperElement `json:"elements"`
	Error    *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type helperClient struct{ path string }

func (client helperClient) call(ctx context.Context, request helperRequest) (helperResponse, error) {
	request.ProtocolVersion = helperProtocolVersion
	input, err := json.Marshal(request)
	if err != nil {
		return helperResponse{}, fmt.Errorf("encode helper request: %w", err)
	}
	command := exec.CommandContext(ctx, client.path)
	command.Stdin = bytes.NewReader(input)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	runErr := command.Run()
	var response helperResponse
	decoder := json.NewDecoder(&stdout)
	if err := decoder.Decode(&response); err != nil {
		return helperResponse{}, fmt.Errorf(
			"decode helper response (exit=%v, stderr=%q): %w",
			runErr,
			strings.TrimSpace(stderr.String()),
			err,
		)
	}
	if runErr != nil || !response.OK {
		if response.Error != nil {
			return response, fmt.Errorf("helper %s: %s", response.Error.Code, response.Error.Message)
		}
		return response, fmt.Errorf("helper failed: %w", runErr)
	}
	return response, nil
}

func (client helperClient) preflight(ctx context.Context) (helperStatus, error) {
	response, err := client.call(ctx, helperRequest{Command: "preflight"})
	return response.Status, err
}

func (client helperClient) inspect(ctx context.Context) ([]helperElement, error) {
	response, err := client.call(ctx, helperRequest{
		Command: "inspect",
		Limits:  map[string]int{"maxDepth": 64, "maxNodes": 5_000},
	})
	return response.Elements, err
}

func (client helperClient) probe(ctx context.Context, controls map[string]helperSelector) error {
	// Probe exactly what the caller resolved. Probing a control the caller did
	// not ask for reintroduces the coupling composerControls exists to remove.
	names := make([]string, 0, len(controls))
	for name := range controls {
		names = append(names, name)
	}
	sort.Strings(names)
	probes := make([]helperProbe, 0, len(names))
	for _, name := range names {
		selector, ok := controls[name]
		if !ok {
			return fmt.Errorf("control catalog has no %s selector", name)
		}
		probes = append(probes, helperProbe{
			Name: name, Selectors: []helperSelector{selector}, Required: true, RequiresGeometry: true,
		})
	}
	_, err := client.call(ctx, helperRequest{
		Command: "probe",
		Limits:  map[string]int{"maxDepth": 64, "maxNodes": 5_000},
		Probes:  probes,
	})
	return err
}

func (client helperClient) probeProject(ctx context.Context, selector helperSelector) error {
	_, err := client.call(ctx, helperRequest{
		Command: "probe",
		Limits:  map[string]int{"maxDepth": 64, "maxNodes": 5_000},
		Probes: []helperProbe{{
			Name: "project", Selectors: []helperSelector{selector},
			Required: true, RequiresGeometry: true,
		}},
	})
	return err
}

func (client helperClient) setValue(ctx context.Context, selector helperSelector, value string) error {
	_, err := client.call(ctx, helperRequest{
		Command:  "set_value",
		Selector: &selector,
		Value:    &value,
		Limits:   map[string]int{"maxDepth": 64, "maxNodes": 5_000},
	})
	return err
}

func (client helperClient) click(
	ctx context.Context,
	selector helperSelector,
	postcondition helperPostcondition,
) error {
	_, err := client.call(ctx, helperRequest{
		Command:       "physical_click",
		Selector:      &selector,
		Postcondition: &postcondition,
		Limits:        map[string]int{"maxDepth": 64, "maxNodes": 5_000},
	})
	return err
}

func selectorFor(element helperElement) helperSelector {
	return helperSelector{
		Role: element.Role, Title: element.Title, Description: element.Description, Hierarchy: element.Hierarchy,
	}
}
