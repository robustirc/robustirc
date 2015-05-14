package outputstream

import (
	"runtime"
	"testing"
	"time"

	"github.com/robustirc/robustirc/types"
)

func addEmptyMsg(os *OutputStream, id, reply int64) {
	os.Add([]*types.RobustMessage{
		{Id: types.RobustId{Id: id, Reply: reply}}})
}

func testBlocking(t *testing.T, os *OutputStream, lastseen types.RobustId, want types.RobustId) {
	next := make(chan []*types.RobustMessage)

	go func() {
		next <- os.GetNext(lastseen, nil)
	}()

	// Make the other goroutine run.
	runtime.Gosched()

	select {
	case <-next:
		t.Fatalf("Read from channel before Add()ing a message")
	default:
	}

	os.Add([]*types.RobustMessage{{Id: want}})

	select {
	case msgs := <-next:
		if msgs[0].Id != want {
			t.Fatalf("got %v, want %v", msgs[0].Id, want)
		}

	case <-time.After(1 * time.Second):
		t.Fatalf("Timeout waiting for os.GetNext() to return")
	}
}

func TestAppendNext(t *testing.T) {
	os := NewOutputStream()

	testBlocking(t, os, types.RobustId{}, types.RobustId{Id: 1, Reply: 1})
}

func TestCatchUp(t *testing.T) {
	os := NewOutputStream()

	if got, want := os.LastSeen(), (types.RobustId{Id: 0, Reply: 0}); got != want {
		t.Fatalf("got %v, want %v", got, want)
	}

	addEmptyMsg(os, 1, 1)
	if got, want := os.LastSeen(), (types.RobustId{Id: 1, Reply: 1}); got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
	addEmptyMsg(os, 2, 1)
	addEmptyMsg(os, 3, 1)

	msgs := os.GetNext(types.RobustId{}, nil)
	if want := (types.RobustId{Id: 1, Reply: 1}); msgs[0].Id != want {
		t.Fatalf("got %v, want %v", msgs[0].Id, want)
	}
	msgs = os.GetNext(msgs[0].Id, nil)
	if want := (types.RobustId{Id: 2, Reply: 1}); msgs[0].Id != want {
		t.Fatalf("got %v, want %v", msgs[0].Id, want)
	}
	msgs = os.GetNext(msgs[0].Id, nil)
	if want := (types.RobustId{Id: 3, Reply: 1}); msgs[0].Id != want {
		t.Fatalf("got %v, want %v", msgs[0].Id, want)
	}
}

func TestDeleteMiddle(t *testing.T) {
	os := NewOutputStream()

	addEmptyMsg(os, 1, 1)
	addEmptyMsg(os, 2, 1)
	addEmptyMsg(os, 3, 1)

	os.Delete(types.RobustId{Id: 2, Reply: 0})

	// Verify we get the expected messages when using Get directly with the
	// input IDs.
	msgs, ok := os.Get(types.RobustId{Id: 3})
	if !ok {
		t.Fatalf("got false, want true")
	}
	if want := (types.RobustId{Id: 3, Reply: 1}); msgs[0].Id != want {
		t.Fatalf("got %v, want %v", msgs[0].Id, want)
	}

	// Verify getting an invalid message works as expected
	msgs, ok = os.Get(types.RobustId{Id: 23})
	if ok {
		t.Fatalf("got true, want false")
	}
	if msgs != nil {
		t.Fatalf("got %v, want nil", msgs)
	}

	// Now get the same messages using GetNext
	msgs = os.GetNext(types.RobustId{Id: 2, Reply: 1}, nil)
	if want := (types.RobustId{Id: 3, Reply: 1}); msgs[0].Id != want {
		t.Fatalf("got %v, want %v", msgs[0].Id, want)
	}

	msgs = os.GetNext(types.RobustId{Id: 1, Reply: 1}, nil)
	if want := (types.RobustId{Id: 3, Reply: 1}); msgs[0].Id != want {
		t.Fatalf("got %v, want %v", msgs[0].Id, want)
	}

	os.Delete(types.RobustId{Id: 3, Reply: 0})

	testBlocking(t, os, msgs[0].Id, types.RobustId{Id: 4, Reply: 1})

	os.Delete(types.RobustId{Id: 1, Reply: 0})
	os.Delete(types.RobustId{Id: 4, Reply: 0})

	testBlocking(t, os, msgs[0].Id, types.RobustId{Id: 5, Reply: 1})

	// Just to get 100% code coverage. We could also not do this, but then a
	// human needs to look at the coverage output and keep the special case of
	// the untested log.Panicf call in mind, so we just put the special case
	// into code here.
	os.Delete(types.RobustId{Id: 5, Reply: 0})
	defer func() {
		if err := recover(); err == nil {
			t.Fatalf("Expected a panic")
		}
	}()
	os.Delete(types.RobustId{Id: 0, Reply: 0})
}

func TestInterrupt(t *testing.T) {
	os := NewOutputStream()

	go func() {
		for range time.NewTicker(1 * time.Millisecond).C {
			os.InterruptGetNext()
		}
	}()

	addEmptyMsg(os, 1, 1)

	msgs := os.GetNext(types.RobustId{}, nil)
	if want := (types.RobustId{Id: 1, Reply: 1}); msgs[0].Id != want {
		t.Fatalf("got %v, want %v", msgs[0].Id, want)
	}

	cancelled1 := false
	cancelled2 := false

	unblocked1 := make(chan bool)
	unblocked2 := make(chan bool)

	go func() {
		msgs = os.GetNext(msgs[0].Id, &cancelled1)
		unblocked1 <- true
	}()

	go func() {
		msgs = os.GetNext(msgs[0].Id, &cancelled2)
		unblocked2 <- true
	}()

	time.Sleep(1 * time.Millisecond)
	select {
	case <-unblocked1:
		t.Fatalf("GetNext() returned before cancelled is true")
	default:
	}
	cancelled1 = true
	select {
	case <-unblocked1:
	case <-time.After(1 * time.Second):
		t.Fatalf("GetNext() did not return after setting cancelled to true")
	}

	select {
	case <-unblocked2:
		t.Fatalf("Second GetNext() returned before cancelled is true")
	default:
	}
}
