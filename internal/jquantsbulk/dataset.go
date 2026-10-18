package jquantsbulk

import "errors"

// Dataset names are a finite read-only allowlist, never arbitrary API paths.
type Dataset string

const (
	Minute Dataset = "minute"
	Daily  Dataset = "daily"
	Master Dataset = "master"
)

func ValidateDataset(value string) (Dataset, error) {
	d := Dataset(value)
	if value == "" || !d.valid() {
		return "", errors.New("bulk-datasetはminute/daily/masterのいずれかで指定してください")
	}
	return d, nil
}

func (d Dataset) valid() bool { return d == "" || d == Minute || d == Daily || d == Master }
func (d Dataset) endpoint() string {
	switch d {
	case "", Minute:
		return Endpoint
	case Daily:
		return "/equities/bars/daily"
	case Master:
		return "/equities/master"
	default:
		return ""
	}
}
