package signal

import (
	"encoding/json"
	"fmt"
)

type Projection struct {
	AsOf       string
	Symbols    []string
	Source     string
	SourceID   string
	Facts      json.RawMessage
	Provenance Provenance
}

type Projector struct{}

func (Projector) Macro(p Projection) (Event, error)         { return project(TypeMacro, p) }
func (Projector) Short(p Projection) (Event, error)         { return project(TypeShortInterest, p) }
func (Projector) ThirteenF(p Projection) (Event, error)     { return project(Type13F, p) }
func (Projector) AnalystRating(p Projection) (Event, error) { return project(TypeAnalystRating, p) }

func project(signalType string, p Projection) (Event, error) {
	id, err := StableID(signalType, p.AsOf, p.Symbols, p.Source, p.Facts)
	if err != nil {
		return Event{}, err
	}
	event := Event{
		Version: Version, SignalID: id, Type: signalType, AsOf: p.AsOf,
		Symbols: append([]string(nil), p.Symbols...), Source: p.Source,
		SourceID: p.SourceID, Facts: append(json.RawMessage(nil), p.Facts...), Provenance: p.Provenance,
	}
	if err := event.Validate(); err != nil {
		return Event{}, fmt.Errorf("signal: project %s: %w", signalType, err)
	}
	return event, nil
}
