package outputstream

import (
	"runtime"
	"testing"
	"time"

	"github.com/robustirc/robustirc/types"
)

func addEmptyMsg(id, reply int64) {
	Add([]*types.RobustMessage{
		&types.RobustMessage{Id: types.RobustId{Id: id, Reply: reply}}})
}

func testBlocking(t *testing.T, lastseen types.RobustId, want types.RobustId) {
	next := make(chan []*types.RobustMessage)

	go func() {
		next <- GetNext(lastseen)
	}()

	// Make the other goroutine run.
	runtime.Gosched()

	select {
	case <-next:
		t.Fatalf("Read from channel before Add()ing a message")
	default:
	}

	Add([]*types.RobustMessage{&types.RobustMessage{Id: want}})

	select {
	case msgs := <-next:
		if msgs[0].Id != want {
			t.Fatalf("got %v, want %v", msgs[0].Id, want)
		}

	case <-time.After(1 * time.Second):
		t.Fatalf("Timeout waiting for GetNext() to return")
	}
}

func TestAppendNext(t *testing.T) {
	Reset()

	testBlocking(t, types.RobustId{}, types.RobustId{Id: 1, Reply: 1})
}

func TestCatchUp(t *testing.T) {
	Reset()

	if got, want := LastSeen(), (types.RobustId{Id: 0, Reply: 0}); got != want {
		t.Fatalf("got %v, want %v", got, want)
	}

	addEmptyMsg(1, 1)
	if got, want := LastSeen(), (types.RobustId{Id: 1, Reply: 1}); got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
	addEmptyMsg(2, 1)
	addEmptyMsg(3, 1)

	msgs := GetNext(types.RobustId{})
	if want := (types.RobustId{Id: 1, Reply: 1}); msgs[0].Id != want {
		t.Fatalf("got %v, want %v", msgs[0].Id, want)
	}
	msgs = GetNext(msgs[0].Id)
	if want := (types.RobustId{Id: 2, Reply: 1}); msgs[0].Id != want {
		t.Fatalf("got %v, want %v", msgs[0].Id, want)
	}
	msgs = GetNext(msgs[0].Id)
	if want := (types.RobustId{Id: 3, Reply: 1}); msgs[0].Id != want {
		t.Fatalf("got %v, want %v", msgs[0].Id, want)
	}
}

func TestDeleteMiddle(t *testing.T) {
	Reset()

	addEmptyMsg(1, 1)
	addEmptyMsg(2, 1)
	addEmptyMsg(3, 1)

	Delete(types.RobustId{Id: 2, Reply: 0})

	msgs := GetNext(types.RobustId{Id: 2, Reply: 1})
	if want := (types.RobustId{Id: 3, Reply: 1}); msgs[0].Id != want {
		t.Fatalf("got %v, want %v", msgs[0].Id, want)
	}

	msgs = GetNext(types.RobustId{Id: 1, Reply: 1})
	if want := (types.RobustId{Id: 3, Reply: 1}); msgs[0].Id != want {
		t.Fatalf("got %v, want %v", msgs[0].Id, want)
	}

	Delete(types.RobustId{Id: 3, Reply: 0})

	testBlocking(t, msgs[0].Id, types.RobustId{Id: 4, Reply: 1})

	Delete(types.RobustId{Id: 1, Reply: 0})
	Delete(types.RobustId{Id: 4, Reply: 0})

	testBlocking(t, msgs[0].Id, types.RobustId{Id: 5, Reply: 1})

	// Just to get 100% code coverage. We could also not do this, but then a
	// human needs to look at the coverage output and keep the special case of
	// the untested log.Panicf call in mind, so we just put the special case
	// into code here.
	Delete(types.RobustId{Id: 5, Reply: 0})
	defer func() {
		if err := recover(); err == nil {
			t.Fatalf("Expected a panic")
		}
	}()
	Delete(types.RobustId{Id: 0, Reply: 0})
}
