package main

import (
	"context"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"time"
)

type excursion struct {
	MFE, MAE, Endpoint, EndpointDeltaJPY        float64
	EarlyGapBPS, EarlyGapJPY, EntryOvershootBPS float64
	EarlyGapAvailable, OvershootAvailable       bool
	Stop, Recovered                             bool
}

// This is an observational original-time-horizon envelope, not an execution
// model. A stopped trade's ambiguous exit bar is excluded from recovery checks.
func inspectPath(e entry, bars []bar, model string) (excursion, error) {
	x := excursion{}
	t := e.Trade
	if t.Entry <= 0 || t.Exit <= 0 || (t.Direction != "long" && t.Direction != "short") || t.EntryAt.Before(clock(e.Date, 540)) {
		return x, fail("Invalid frozen trade for path diagnosis")
	}
	deadline := t.EntryAt.Add(20 * time.Minute)
	if model == models[1] {
		deadline = clock(e.Date, 600)
		if !t.EntryAt.Equal(clock(e.Date, 570)) || !t.ExitAt.Equal(deadline) {
			return x, fail("NA fixed clock differs")
		}
	} else if model == models[2] {
		deadline = clock(e.Date, 570)
		if !t.EntryAt.Equal(clock(e.Date, 541)) || !t.ExitAt.Equal(deadline) {
			return x, fail("GAP fixed clock differs")
		}
	} else if model != models[0] {
		return x, fail("Unknown fixed model")
	}
	if !deadline.After(t.EntryAt) || t.ExitAt.Before(t.EntryAt) || t.ExitAt.After(deadline) {
		return x, fail("Invalid original trade clock")
	}
	byTime := map[time.Time]bar{}
	for _, b := range bars {
		key := b.At.UTC()
		if _, dup := byTime[key]; dup {
			return x, fail("Duplicate path minute")
		}
		byTime[key] = b
	}
	last, exists := byTime[deadline.UTC()]
	first, firstOK := byTime[t.EntryAt.UTC()]
	if !exists || !firstOK || first.Open != t.Entry || len(last.Issues) > 0 {
		return x, fail("Original endpoint or entry unavailable")
	}
	sign := float64(1)
	if t.Direction == "short" {
		sign = -1
	}
	move := func(price int64) float64 { return sign * float64(price-t.Entry) / float64(t.Entry) * 10000 }
	x.Stop = strings.Contains(t.Reason, "stop")
	for at := t.EntryAt; at.Before(deadline); at = at.Add(time.Minute) {
		b, ok := byTime[at.UTC()]
		if !ok || len(b.Issues) > 0 {
			return x, fail("Fixed original horizon minute missing")
		}
		high, low := move(b.High), move(b.Low)
		if high < low {
			high, low = low, high
		}
		x.MFE = math.Max(x.MFE, high)
		x.MAE = math.Min(x.MAE, low)
		if x.Stop && !at.Before(t.ExitAt) && high >= 0 {
			x.Recovered = true
		}
	}
	x.Endpoint = move(last.Open)
	x.MFE = math.Max(x.MFE, x.Endpoint)
	x.MAE = math.Min(x.MAE, x.Endpoint)
	if x.Stop && x.Endpoint >= 0 {
		x.Recovered = true
	}
	x.EndpointDeltaJPY = sign * float64(last.Open-t.Exit) / 100
	if model == models[2] {
		opening, ok := byTime[clock(e.Date, 540).UTC()]
		if !ok || len(opening.Issues) > 0 {
			return x, fail("GAP original signal minute missing")
		}
		x.EarlyGapAvailable = true
		x.EarlyGapBPS = sign * float64(t.Entry-opening.Open) / float64(opening.Open) * 10000
		x.EarlyGapJPY = sign * float64(t.Entry-opening.Open) / 100
	}
	if model == models[0] {
		hi, lo := int64(0), int64(0)
		for m := 540; m <= 544; m++ {
			if b, ok := byTime[clock(e.Date, m).UTC()]; ok {
				if b.High > hi {
					hi = b.High
				}
				if lo == 0 || b.Low < lo {
					lo = b.Low
				}
			}
		}
		if hi > 0 && lo > 0 {
			boundary := hi
			if t.Direction == "short" {
				boundary = lo
			}
			x.OvershootAvailable = true
			x.EntryOvershootBPS = sign * float64(t.Entry-boundary) / float64(t.Entry) * 10000
		}
	}
	return x, nil
}

// Exactly n indices, fixed-length circular blocks. All models use the same
// sampled indices, preserving their observed within-day cross dependence.
func sampleIndices(r *rand.Rand, n, block int) []int {
	v := make([]int, 0, n)
	for len(v) < n {
		start := r.IntN(n)
		for j := 0; j < block && len(v) < n; j++ {
			v = append(v, (start+j)%n)
		}
	}
	return v
}
func quantile(v []float64, p float64) float64 {
	at := p * float64(len(v)-1)
	lo := int(math.Floor(at))
	hi := int(math.Ceil(at))
	return v[lo] + (v[hi]-v[lo])*(at-float64(lo))
}
func bootstrap(ctx context.Context, series [][]float64, fees []float64, block, reps int, seed uint64) ([][2]float64, [][2]float64, error) {
	if len(series) != 3 || len(fees) != 3 || len(series[0]) < block || block < 1 || reps < 2 {
		return nil, nil, fail("Invalid fixed bootstrap dimensions")
	}
	n := len(series[0])
	for _, s := range series {
		if len(s) != n {
			return nil, nil, fail("Paired session lengths differ")
		}
	}
	draws := make([][]float64, 6)
	for i := range draws {
		draws[i] = make([]float64, reps)
	}
	r := rand.New(rand.NewPCG(seed, uint64(block)))
	for b := 0; b < reps; b++ {
		if ctx.Err() != nil {
			return nil, nil, fail("Loss analysis interrupted")
		}
		ix := sampleIndices(r, n, block)
		v := [3]float64{}
		for m, s := range series {
			for _, i := range ix {
				v[m] += s[i]
			}
			v[m] -= fees[m]
			draws[m][b] = v[m]
		}
		draws[3][b] = v[0] - v[1]
		draws[4][b] = v[0] - v[2]
		draws[5][b] = v[1] - v[2]
	}
	bounds := make([][2]float64, 6)
	for i, d := range draws {
		sort.Float64s(d)
		bounds[i] = [2]float64{quantile(d, .025), quantile(d, .975)}
	}
	return bounds[:3], bounds[3:], nil
}

func groupKeys(e entry) []string {
	rank := "01-10"
	if e.Rank > 10 {
		rank = "11-20"
	}
	if e.Rank > 20 {
		rank = "21-30"
	}
	n := float64(e.Trade.Entry) / 100
	notional := "<=100000"
	if n > 100000 {
		notional = "100000..500000"
	}
	if n > 500000 {
		notional = "500000..1000000"
	}
	if n > 1000000 {
		notional = ">1000000"
	}
	fit := "false"
	if e.Fits {
		fit = "true"
	}
	return []string{"all", "status/" + e.Status, "direction/" + e.Trade.Direction, "rank/" + rank, "notional/" + notional, "fits_independent/" + fit}
}
