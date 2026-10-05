package ui

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// A chart is drawn by the server as SVG: one series over time, as a line
// with a faint fill under it. There is no script and no library. The
// figures a reader needs (the latest value, the average, the highest) are
// written out as text beside it, and each point says its own time and
// value when the pointer rests on it.

// ChartPoint is one value at a time (Unix seconds).
type ChartPoint struct {
	At int64
	V  float64
}

// ChartProps describes a chart.
type ChartProps struct {
	Title  string
	Points []ChartPoint // oldest first
	// From and To are the stretch of time the chart covers, whether or
	// not there are points all the way.
	From, To int64
	// Step is how far apart points are when nothing is missing. A wider
	// gap breaks the line: the server was not sampled then, and a line
	// drawn across would say it was.
	Step int64
	// Top is the top of the scale. Zero takes it from the data.
	Top float64
	// Format writes a value with its unit.
	Format func(float64) string
	// Note is added after the figures, such as a limit.
	Note string
}

// The drawing's own coordinates. The SVG is stretched to its box, so only
// the proportions matter; strokes keep their width.
const (
	chartW   = 600.0
	chartH   = 120.0
	chartPad = 4.0 // above the top of the scale, so a line there is whole
)

// chartHit is the strip around one point that answers the pointer.
type chartHit struct {
	X, W  string
	Label string
}

// chartLayout is a chart worked out: what the template writes.
type chartLayout struct {
	Lines []string // the points of each unbroken stretch
	Areas []string // the fill under each
	Hits  []chartHit
	// Latest is the newest value; Figures the average and the highest.
	Latest, Figures    string
	TopLabel, MidLabel string
	FromLabel, ToLabel string
	Summary            string
}

func coord(v float64) string { return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64) }

// niceTop rounds a value up to 1, 2 or 5 times a power of ten.
func niceTop(v float64) float64 {
	if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 1
	}
	pow := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 5, 10} {
		if v <= m*pow {
			return m * pow
		}
	}
	return 10 * pow
}

// clock writes a time of day. The server does not know the reader's time
// zone, so it says which one it used.
func clock(at int64) string { return time.Unix(at, 0).UTC().Format("15:04") + " UTC" }

func (p ChartProps) layout() chartLayout {
	var l chartLayout
	format := p.Format
	if format == nil {
		format = func(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
	}
	if len(p.Points) == 0 || p.To <= p.From {
		return l
	}
	top, sum, high := p.Top, 0.0, 0.0
	for _, pt := range p.Points {
		sum += pt.V
		high = math.Max(high, pt.V)
	}
	if top <= 0 {
		top = niceTop(high)
	}
	span := float64(p.To - p.From)
	x := func(at int64) float64 {
		return math.Min(chartW, math.Max(0, float64(at-p.From)/span*chartW))
	}
	y := func(v float64) float64 {
		return chartH - math.Min(1, math.Max(0, v/top))*(chartH-chartPad)
	}
	step := p.Step
	if step <= 0 {
		step = 60
	}

	var line, area strings.Builder
	first, last := 0.0, 0.0
	flush := func(n int) {
		if n == 0 {
			return
		}
		if n == 1 {
			// One point alone would draw nothing: give it a little width.
			pt := strings.TrimSpace(line.String())
			_, py, _ := strings.Cut(pt, ",")
			line.Reset()
			line.WriteString(coord(math.Max(0, first-3)) + "," + py + " " + coord(math.Min(chartW, first+3)) + "," + py)
		}
		l.Lines = append(l.Lines, strings.TrimSpace(line.String()))
		l.Areas = append(l.Areas, "M"+coord(first)+","+coord(chartH)+" L"+strings.ReplaceAll(strings.TrimSpace(line.String()), " ", " L")+" L"+coord(last)+","+coord(chartH)+" Z")
		line.Reset()
		area.Reset()
	}
	run := 0
	for i, pt := range p.Points {
		if i > 0 && pt.At-p.Points[i-1].At > 2*step {
			flush(run)
			run = 0
		}
		px := x(pt.At)
		if run == 0 {
			first = px
		}
		last = px
		line.WriteString(coord(px) + "," + coord(y(pt.V)) + " ")
		run++

		// The strip reaches half way to each neighbour.
		left, right := px-chartW*float64(step)/span/2, px+chartW*float64(step)/span/2
		if i > 0 {
			left = math.Max(left, (x(p.Points[i-1].At)+px)/2)
		}
		if i < len(p.Points)-1 {
			right = math.Min(right, (px+x(p.Points[i+1].At))/2)
		}
		left, right = math.Max(0, left), math.Min(chartW, right)
		l.Hits = append(l.Hits, chartHit{X: coord(left), W: coord(math.Max(1, right-left)), Label: clock(pt.At) + " · " + format(pt.V)})
	}
	flush(run)

	newest := p.Points[len(p.Points)-1]
	l.Latest = format(newest.V)
	l.Figures = "average " + format(sum/float64(len(p.Points))) + " · highest " + format(high)
	if p.Note != "" {
		l.Figures += " · " + p.Note
	}
	l.TopLabel, l.MidLabel = format(top), format(top/2)
	l.FromLabel, l.ToLabel = clock(p.From), clock(p.To)
	l.Summary = p.Title + " from " + l.FromLabel + " to " + l.ToLabel + ": latest " + l.Latest + ", " + l.Figures
	return l
}

// Percent writes a share with at most one decimal.
func Percent(v float64) string {
	if v >= 100 || v == math.Trunc(v) {
		return strconv.FormatFloat(v, 'f', 0, 64) + "%"
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + "%"
}

// Bytes writes an amount of memory or disk in the largest unit that keeps
// it readable.
func Bytes(v float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 || v >= 100 {
		return strconv.FormatFloat(v, 'f', 0, 64) + " " + units[i]
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + " " + units[i]
}

// Load writes a load average.
func Load(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

// BytesTop is the top of the scale for amounts in bytes: the largest of
// the points, rounded up to a round figure in the unit it will be read in.
func BytesTop(points []ChartPoint) float64 {
	high := 0.0
	for _, pt := range points {
		high = math.Max(high, pt.V)
	}
	unit := 1.0
	for high/unit >= 1024 {
		unit *= 1024
	}
	return niceTop(high/unit) * unit
}
