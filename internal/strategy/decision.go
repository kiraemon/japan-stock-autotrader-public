// Package strategy defines the values shared by decision makers and execution.
package strategy

import "time"

type Direction string
type Variant string

const (
	Long  Direction = "long"
	Short Direction = "short"
)

func ValidDirection(direction Direction) bool {
	return direction == Long || direction == Short
}

// Decision describes a completed-bar decision. A non-accepted decision is a
// skip unless Stop is set. Boundary is a reference price for the strategy.
// The report retains the original diagnostic fields.
type Decision struct {
	At       time.Time       `json:"signal_at"`
	Boundary int64           `json:"range_boundary"`
	Accepted bool            `json:"accepted"`
	Reason   string          `json:"reason"`
	Volume   *VolumeDecision `json:"volume_filter,omitempty"`
	Stop     bool            `json:"-"`
}

// VolumeDecision preserves the volume diagnostic in existing reports.
type VolumeDecision struct {
	Passed       bool   `json:"passed"`
	Reason       string `json:"reason"`
	SignalVolume *int64 `json:"signal_volume_shares"`
	PreviousSum  string `json:"previous_5_volume_sum"`
}

func (d Decision) Clone() Decision {
	if d.Volume != nil {
		volume := *d.Volume
		if volume.SignalVolume != nil {
			quantity := *volume.SignalVolume
			volume.SignalVolume = &quantity
		}
		d.Volume = &volume
	}
	return d
}
