package main

import "testing"

func TestPathSummarySeparatesMissingOptionalValuesAndRecovery(t *testing.T) {
	var summary pathSummary
	for _, path := range []excursion{
		{EarlyGapAvailable: true, EarlyGapBPS: 20, EarlyGapJPY: 100, OvershootAvailable: true, EntryOvershootBPS: 12, MFE: 60, MAE: -20, Endpoint: 10, Stop: true, Recovered: true, EndpointDeltaJPY: 40},
		{MFE: 0, MAE: -40, Endpoint: -20},
		{EarlyGapAvailable: true, EarlyGapBPS: -10, EarlyGapJPY: -30, MFE: 30, MAE: -60, Endpoint: -20, Stop: true, EndpointDeltaJPY: -10},
	} {
		summary.add(path)
	}
	if summary.Count != 3 || summary.EarlyGapCount != 2 || summary.EarlyGapMean != 5 ||
		summary.EarlyGapSum != 70 || summary.OvershootCount != 1 || summary.OvershootMean != 12 {
		t.Fatalf("optional measurements used the wrong denominator: %+v", summary)
	}
	if summary.MFE != 30 || summary.MAE != -40 || summary.Endpoint != -10 ||
		summary.PositivePeak != 2 || summary.PositiveEndpoint != 1 || summary.Stops != 2 ||
		summary.Recovered != 1 || summary.RecoveredAtEnd != 1 || summary.EndpointMinusExit != 30 {
		t.Fatalf("stop recovery or path totals changed: %+v", summary)
	}
}
