package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

type interval struct {
	ElapsedNS        int64
	CPUNS, DiskBytes uint64
}
type result struct {
	FixtureDigest                                          string
	ElapsedNS                                              int64
	CPUNS, DiskBytes                                       uint64
	CommitCallbacks                                        int64
	RequestLatencyNS, CompletionLatencyNS, StatusLatencyNS []int64
	QueuePeak                                              int
	OldestPendingNS                                        int64
	Retries                                                map[int64]int
	Intervals                                              []interval
	Files                                                  map[string]int64
	SeededReceipts, CompletedTraceReceipts                 int
	Failures                                               []string
}

func main() {
	if len(os.Args) != 2 {
		panic("provide qualification output directory")
	}
	fmt.Println("# Audit qualification results\n\nEach numeric cell reports the median of successful independent processes. Interval cells also report how many processes produced a complete sample. Baseline failures invalidate the acceptance verdict for that case. CPU and disk counts cover the measured process; synchronization calls remain unmeasured.")
	for _, history := range []string{"empty", "seven"} {
		for _, scenario := range []string{"burst", "steady", "status", "cancellation", "backlog"} {
			baseline, baselineFailures := readResults(os.Args[1], "baseline", history, scenario)
			candidate, candidateFailures := readResults(os.Args[1], "candidate", history, scenario)
			fmt.Printf("\n## %s / %s\n\nBaseline: %d passed, %d failed. Candidate: %d passed, %d failed.\n\n", history, scenario, len(baseline), baselineFailures, len(candidate), candidateFailures)
			if len(baseline) == 0 || len(candidate) == 0 {
				fmt.Println("No paired comparison is available.")
				continue
			}
			fmt.Println("| Metric | Baseline median | Baseline min..max | Candidate median | Verdict |\n| --- | ---: | --- | ---: | --- |")
			valid := len(baseline) == 5 && len(candidate) == 5 && baselineFailures == 0 && candidateFailures == 0
			row("CPU ns/event", values(baseline, func(r result) float64 { return float64(r.CPUNS) / 1200 }), values(candidate, func(r result) float64 { return float64(r.CPUNS) / 1200 }), "improve", valid)
			row("Physical write bytes/event", values(baseline, func(r result) float64 { return float64(r.DiskBytes) / 1200 }), values(candidate, func(r result) float64 { return float64(r.DiskBytes) / 1200 }), "improve", valid)
			row("Request median ns", values(baseline, func(r result) float64 { return quantile(r.RequestLatencyNS, 50) }), values(candidate, func(r result) float64 { return quantile(r.RequestLatencyNS, 50) }), "latency", valid)
			row("Request p95 ns", values(baseline, func(r result) float64 { return quantile(r.RequestLatencyNS, 95) }), values(candidate, func(r result) float64 { return quantile(r.RequestLatencyNS, 95) }), "descriptive", valid)
			row("Request p99 ns", values(baseline, func(r result) float64 { return quantile(r.RequestLatencyNS, 99) }), values(candidate, func(r result) float64 { return quantile(r.RequestLatencyNS, 99) }), "descriptive", valid)
			row("Completion p95 ns", values(baseline, func(r result) float64 { return quantile(r.CompletionLatencyNS, 95) }), values(candidate, func(r result) float64 { return quantile(r.CompletionLatencyNS, 95) }), "descriptive", valid)
			row("Completion p99 ns", values(baseline, func(r result) float64 { return quantile(r.CompletionLatencyNS, 99) }), values(candidate, func(r result) float64 { return quantile(r.CompletionLatencyNS, 99) }), "descriptive", valid)
			row("Trace completions/second", values(baseline, func(r result) float64 { return 1200e9 / float64(r.ElapsedNS) }), values(candidate, func(r result) float64 { return 1200e9 / float64(r.ElapsedNS) }), "throughput", valid)
			row("Current writer commit callbacks", values(baseline, func(r result) float64 { return float64(r.CommitCallbacks) }), values(candidate, func(r result) float64 { return float64(r.CommitCallbacks) }), "descriptive", valid)
			row("Sampled queue peak", values(baseline, func(r result) float64 { return float64(r.QueuePeak) }), values(candidate, func(r result) float64 { return float64(r.QueuePeak) }), "descriptive", valid)
			row("Oldest trace pending ns", values(baseline, func(r result) float64 { return float64(r.OldestPendingNS) }), values(candidate, func(r result) float64 { return float64(r.OldestPendingNS) }), "descriptive", valid)
			row("Max retries/observed receipt", values(baseline, maximumRetries), values(candidate, maximumRetries), "descriptive", valid)
			row("Final bytes/retained receipt", values(baseline, retainedBytes), values(candidate, retainedBytes), "descriptive", valid)
			optionalRow("Worst sampled 5s CPU ns", baseline, candidate, peakCPU)
			optionalRow("Worst sampled 5s disk bytes", baseline, candidate, peakDisk)
		}
	}
}

func readResults(root, variant, history, scenario string) ([]result, int) {
	var results []result
	failures := 0
	for repetition := 1; repetition <= 5; repetition++ {
		path := filepath.Join(root, fmt.Sprintf("%s-%s-%s-%d.json", variant, history, scenario, repetition))
		body, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			failures++
			continue
		}
		var current result
		if err := json.Unmarshal(body, &current); err != nil {
			panic(err)
		}
		if current.FixtureDigest != "90d8212fbde8ac38fea628f34bb7851daed7039e709d08991df6f48aa3e5ee91" {
			panic("trace digest differs")
		}
		if len(current.Failures) > 0 {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, current.Failures)
			failures++
			continue
		}
		results = append(results, current)
	}
	return results, failures
}

func values(results []result, extract func(result) float64) []float64 {
	values := make([]float64, len(results))
	for i, current := range results {
		values[i] = extract(current)
	}
	slices.Sort(values)
	return values
}

func median(values []float64) float64 {
	return (values[(len(values)-1)/2] + values[len(values)/2]) / 2
}

func row(name string, baseline, candidate []float64, rule string, valid bool) {
	base, after := median(baseline), median(candidate)
	spread := baseline[len(baseline)-1] - baseline[0]
	verdict := "descriptive"
	if rule != "descriptive" {
		verdict = "UNPROVEN"
		if valid {
			passed := rule == "improve" && base-after > spread || rule == "latency" && after <= base+spread || rule == "throughput" && after >= base-spread
			verdict = "FAIL"
			if passed {
				verdict = "PASS"
			}
		}
	}
	fmt.Printf("| %s | %.2f | %.2f..%.2f | %.2f | %s |\n", name, base, baseline[0], baseline[len(baseline)-1], after, verdict)
}

func optionalRow(
	name string,
	baselineResults []result,
	candidateResults []result,
	extract func(result) (float64, bool),
) {
	baseline := optionalValues(baselineResults, extract)
	candidate := optionalValues(candidateResults, extract)
	fmt.Printf(
		"| %s | %s | %s | %s | descriptive |\n",
		name,
		formatOptionalMedian(baseline, len(baselineResults)),
		formatOptionalRange(baseline, len(baselineResults)),
		formatOptionalMedian(candidate, len(candidateResults)),
	)
}

func optionalValues(
	results []result,
	extract func(result) (float64, bool),
) []float64 {
	values := make([]float64, 0, len(results))
	for _, current := range results {
		value, available := extract(current)
		if available {
			values = append(values, value)
		}
	}
	slices.Sort(values)
	return values
}

func formatOptionalMedian(values []float64, total int) string {
	if len(values) == 0 {
		return fmt.Sprintf("unavailable (0/%d sampled)", total)
	}
	return fmt.Sprintf("%.2f (%d/%d sampled)", median(values), len(values), total)
}

func formatOptionalRange(values []float64, total int) string {
	if len(values) == 0 {
		return fmt.Sprintf("unavailable (0/%d sampled)", total)
	}
	return fmt.Sprintf(
		"%.2f..%.2f (%d/%d sampled)",
		values[0], values[len(values)-1], len(values), total,
	)
}

func quantile(values []int64, percent int) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := slices.Clone(values)
	slices.Sort(ordered)
	return float64(ordered[(len(ordered)*percent+99)/100-1])
}

func maximumRetries(current result) float64 {
	maximum := 0
	for _, retries := range current.Retries {
		maximum = max(maximum, retries)
	}
	return float64(maximum)
}
func retainedBytes(current result) float64 {
	var total int64
	for _, size := range current.Files {
		total += size
	}
	return float64(total) / float64(1200+current.SeededReceipts)
}
func peakCPU(current result) (float64, bool) {
	if len(current.Intervals) == 0 {
		return 0, false
	}
	maximum := current.Intervals[0].CPUNS
	for _, interval := range current.Intervals[1:] {
		maximum = max(maximum, interval.CPUNS)
	}
	return float64(maximum), true
}
func peakDisk(current result) (float64, bool) {
	if len(current.Intervals) == 0 {
		return 0, false
	}
	maximum := current.Intervals[0].DiskBytes
	for _, interval := range current.Intervals[1:] {
		maximum = max(maximum, interval.DiskBytes)
	}
	return float64(maximum), true
}
