package timesafeguard

import (
	"testing"
	"time"
)

func TestGood(t *testing.T) {
	if !timeInSync([]timeResult{
		{
			Start:  time.Unix(1432323893, 0),
			End:    time.Unix(1432323893, 500*int64(time.Millisecond)),
			Result: time.Unix(1432323893, 1),
		},
		{
			Start:  time.Unix(1432323893, 0),
			End:    time.Unix(1432323893, 500*int64(time.Millisecond)),
			Result: time.Unix(1432323893, 2),
		},
	}) {
		t.Fatalf("foo")
	}
}

func TestBarelyGoodEnough(t *testing.T) {
	if !timeInSync([]timeResult{
		{
			Start:  time.Unix(1432323893, 0),
			End:    time.Unix(1432323894, 500*int64(time.Millisecond)),
			Result: time.Unix(1432323893, 499),
		},
		{
			Start:  time.Unix(1432323893, 0),
			End:    time.Unix(1432323893, 500*int64(time.Millisecond)),
			Result: time.Unix(1432323893, 2),
		},
	}) {
		t.Fatalf("foo")
	}
}

func TestLocalBefore(t *testing.T) {
	if timeInSync([]timeResult{
		{
			Start:  time.Unix(1432321893, 0),
			End:    time.Unix(1432321893, 500*int64(time.Millisecond)),
			Result: time.Unix(1432323893, 1),
		},
		{
			Start:  time.Unix(1432321893, 0),
			End:    time.Unix(1432321893, 500*int64(time.Millisecond)),
			Result: time.Unix(1432323894, 1),
		},
	}) {
		t.Fatalf("foo")
	}
}

func TestLocalAfter(t *testing.T) {
	if timeInSync([]timeResult{
		{
			Start:  time.Unix(1432324893, 0),
			End:    time.Unix(1432324893, 500*int64(time.Millisecond)),
			Result: time.Unix(1432323893, 1),
		},
		{
			Start:  time.Unix(1432324893, 0),
			End:    time.Unix(1432324893, 500*int64(time.Millisecond)),
			Result: time.Unix(1432323893, 5),
		},
	}) {
		t.Fatalf("foo")
	}
}

func TestLocalAfterOne(t *testing.T) {
	if timeInSync([]timeResult{
		{
			Start:  time.Unix(1432324893, 0),
			End:    time.Unix(1432324893, 500*int64(time.Millisecond)),
			Result: time.Unix(1432324893, 1),
		},
		{
			Start:  time.Unix(1432324893, 0),
			End:    time.Unix(1432324893, 500*int64(time.Millisecond)),
			Result: time.Unix(1432323893, 5),
		},
	}) {
		t.Fatalf("foo")
	}
}

func TestLongMeasurement(t *testing.T) {
	if timeInSync([]timeResult{
		{
			Start:  time.Unix(1432324893, 0),
			End:    time.Unix(1432324895, 500*int64(time.Millisecond)),
			Result: time.Unix(1432324893, 5),
		},
		{
			Start:  time.Unix(1432324893, 0),
			End:    time.Unix(1432324893, 500*int64(time.Millisecond)),
			Result: time.Unix(1432324893, 5),
		},
	}) {
		t.Fatalf("foo")
	}
}
