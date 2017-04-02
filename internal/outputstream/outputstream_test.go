package outputstream

import (
	"runtime"
	"testing"
	"time"

	"github.com/robustirc/robustirc/internal/robust"

	"golang.org/x/net/context"
)

func addEmptyMsg(os *OutputStream, id, reply uint64) {
	os.Add([]Message{
		{Id: robust.Id{Id: id, Reply: reply}}})
}

func testBlocking(t *testing.T, os *OutputStream, lastseen robust.Id, want robust.Id) {
	next := make(chan []Message)

	go func() {
		next <- os.GetNext(context.TODO(), lastseen)
	}()

	// Make the other goroutine run.
	runtime.Gosched()

	select {
	case <-next:
		t.Fatalf("Read from channel before Add()ing a message")
	default:
	}

	os.Add([]Message{{Id: want}})

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
	os, err := NewOutputStream("")
	if err != nil {
		t.Fatal(err)
	}

	testBlocking(t, os, robust.Id{}, robust.Id{Id: 1, Reply: 1})
}

func TestCatchUp(t *testing.T) {
	os, err := NewOutputStream("")
	if err != nil {
		t.Fatal(err)
	}

	if got, want := os.LastSeen(), (robust.Id{Id: 0, Reply: 0}); got != want {
		t.Fatalf("got %v, want %v", got, want)
	}

	addEmptyMsg(os, 1, 1)
	if got, want := os.LastSeen(), (robust.Id{Id: 1, Reply: 1}); got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
	addEmptyMsg(os, 2, 1)
	addEmptyMsg(os, 3, 1)

	msgs := os.GetNext(context.TODO(), robust.Id{})
	if want := (robust.Id{Id: 1, Reply: 1}); msgs[0].Id != want {
		t.Fatalf("got %v, want %v", msgs[0].Id, want)
	}
	msgs = os.GetNext(context.TODO(), msgs[0].Id)
	if want := (robust.Id{Id: 2, Reply: 1}); msgs[0].Id != want {
		t.Fatalf("got %v, want %v", msgs[0].Id, want)
	}
	msgs = os.GetNext(context.TODO(), msgs[0].Id)
	if want := (robust.Id{Id: 3, Reply: 1}); msgs[0].Id != want {
		t.Fatalf("got %v, want %v", msgs[0].Id, want)
	}
}

func TestDeleteMiddle(t *testing.T) {
	os, err := NewOutputStream("")
	if err != nil {
		t.Fatal(err)
	}

	addEmptyMsg(os, 1, 1)
	addEmptyMsg(os, 2, 1)
	addEmptyMsg(os, 3, 1)

	os.Delete(robust.Id{Id: 2, Reply: 0})

	// Verify we get the expected messages when using Get directly with the
	// input IDs.
	msgs, ok := os.Get(robust.Id{Id: 3})
	if !ok {
		t.Fatalf("got false, want true")
	}
	if want := (robust.Id{Id: 3, Reply: 1}); msgs[0].Id != want {
		t.Fatalf("got %v, want %v", msgs[0].Id, want)
	}

	// Verify getting an invalid message works as expected
	msgs, ok = os.Get(robust.Id{Id: 23})
	if ok {
		t.Fatalf("got true, want false")
	}
	if msgs != nil {
		t.Fatalf("got %v, want nil", msgs)
	}

	// Now get the same messages using GetNext
	msgs = os.GetNext(context.TODO(), robust.Id{Id: 2, Reply: 1})
	if want := (robust.Id{Id: 3, Reply: 1}); msgs[0].Id != want {
		t.Fatalf("got %v, want %v", msgs[0].Id, want)
	}

	msgs = os.GetNext(context.TODO(), robust.Id{Id: 1, Reply: 1})
	if want := (robust.Id{Id: 3, Reply: 1}); msgs[0].Id != want {
		t.Fatalf("got %v, want %v", msgs[0].Id, want)
	}

	os.Delete(robust.Id{Id: 3, Reply: 0})

	testBlocking(t, os, msgs[0].Id, robust.Id{Id: 4, Reply: 1})

	os.Delete(robust.Id{Id: 1, Reply: 0})
	os.Delete(robust.Id{Id: 4, Reply: 0})

	testBlocking(t, os, msgs[0].Id, robust.Id{Id: 5, Reply: 1})

	// Just to get 100% code coverage. We could also not do this, but then a
	// human needs to look at the coverage output and keep the special case of
	// the untested log.Panicf call in mind, so we just put the special case
	// into code here.
	os.Delete(robust.Id{Id: 5, Reply: 0})
	defer func() {
		if err := recover(); err == nil {
			t.Fatalf("Expected a panic")
		}
	}()
	os.Delete(robust.Id{Id: 0, Reply: 0})
}

func TestInterrupt(t *testing.T) {
	os, err := NewOutputStream("")
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		for range time.NewTicker(1 * time.Millisecond).C {
			os.InterruptGetNext()
		}
	}()

	addEmptyMsg(os, 1, 1)

	msgs := os.GetNext(context.TODO(), robust.Id{})
	if want := (robust.Id{Id: 1, Reply: 1}); msgs[0].Id != want {
		t.Fatalf("got %v, want %v", msgs[0].Id, want)
	}

	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, _ := context.WithCancel(context.Background())

	unblocked1 := make(chan bool)
	unblocked2 := make(chan bool)

	go func() {
		msgs = os.GetNext(ctx1, msgs[0].Id)
		unblocked1 <- true
	}()

	go func() {
		msgs = os.GetNext(ctx2, msgs[0].Id)
		unblocked2 <- true
	}()

	time.Sleep(1 * time.Millisecond)
	select {
	case <-unblocked1:
		t.Fatalf("GetNext() returned before cancelled is true")
	default:
	}
	cancel1()
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
