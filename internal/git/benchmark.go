package git

import (
	"time"
)

// BenchmarkSample is one timed git command, in milliseconds.
type BenchmarkSample struct {
	Name string
	Args []string
	MS   int64
}

// BenchmarkResult is what the performance benchmark shows: the time of
// each measurement, the latency of the connection to the repository (the
// time of a command that does no work), and the score from 0 to 100 the
// two make.
type BenchmarkResult struct {
	Samples   []BenchmarkSample
	LatencyMS int64
	Score     int
}

// latencyArgs is the command that measures the latency: it reads one
// reference and does nothing else, so over SSH its time is the round trip.
var latencyArgs = []string{"rev-parse", "HEAD"}

// LatencyArgs is the git command whose time is the system latency.
func LatencyArgs() []string { return append([]string(nil), latencyArgs...) }

// Benchmark times the commands the performance window measures: the
// changed files, the references and the commit list, after one that
// measures the latency. Each is run twice; the second time is what is
// kept, after the first has warmed the repository.
func (r *Repo) Benchmark() (BenchmarkResult, error) {
	commands := []BenchmarkSample{
		{Name: "Reading changed files", Args: []string{"status", "--porcelain"}},
		{Name: "Reading branches and tags", Args: []string{"for-each-ref"}},
		{Name: "Reading commits", Args: []string{"log", "--oneline", "--all"}},
	}
	timed := func(args []string) (int64, error) {
		var kept time.Duration
		for pass := 0; pass < 2; pass++ {
			start := time.Now()
			if _, err := r.run(slow, args...); err != nil {
				return 0, err
			}
			kept = time.Since(start)
		}
		return kept.Milliseconds(), nil
	}
	var res BenchmarkResult
	latency, err := timed(latencyArgs)
	if err != nil {
		return res, err
	}
	res.LatencyMS = latency
	for _, command := range commands {
		ms, err := timed(command.Args)
		if err != nil {
			return res, err
		}
		command.MS = ms
		res.Samples = append(res.Samples, command)
	}
	res.Score = BenchmarkScore(res.Samples, res.LatencyMS)
	return res, nil
}

// scoreHalf is the time a measurement can take, beyond the latency, for
// the score to be 50.
const scoreHalf = 300

// BenchmarkScore rates the measurements from 0 to 100: a measurement
// that takes no longer than the latency is worth 100, one that takes
// scoreHalf ms more is worth 50, and the score is the mean.
func BenchmarkScore(samples []BenchmarkSample, latencyMS int64) int {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		extra := float64(max(s.MS-latencyMS, 0))
		sum += 100 / (1 + extra/scoreHalf)
	}
	return int(sum/float64(len(samples)) + 0.5)
}
