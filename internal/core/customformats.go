package core

import "encoding/json"

// CustomFormat retains the native TRaSH/Radarr/Sonarr JSON for owner editing.
// Unknown specifications are rejected at import rather than silently matching.
type CustomFormat struct {
	ID             string                `json:"trash_id"`
	Name           string                `json:"name"`
	Scores         map[string]int        `json:"trash_scores,omitempty"`
	Specifications []FormatSpecification `json:"specifications"`
}

type FormatSpecification struct {
	Name           string                     `json:"name"`
	Implementation string                     `json:"implementation"`
	Negate         bool                       `json:"negate"`
	Required       bool                       `json:"required"`
	Fields         map[string]json.RawMessage `json:"fields"`
}
