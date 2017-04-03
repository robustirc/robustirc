package outputstream

import (
	"log"
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

	log.Printf("add")
	os.Add([]Message{{Id: want}})
	log.Printf("after add")

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
	os, err := NewOutputStream("", 5)
	if err != nil {
		t.Fatal(err)
	}

	log.Printf("before")
	testBlocking(t, os, robust.Id{}, robust.Id{Id: 1, Reply: 1})
	log.Printf("after")
}

func TestDisk(t *testing.T) {
	os, err := NewOutputStream("", 5)
	if err != nil {
		t.Fatal(err)
	}

	for i := 1; i < 10; i++ {
		addEmptyMsg(os, uint64(i), 1)
	}

	msgs := make([]Message, 1)
	for i := 1; i < 10; i++ {
		log.Printf("i = %d", i)
		msgs = os.GetNext(context.TODO(), msgs[0].Id)
		if want := (robust.Id{Id: uint64(i), Reply: 1}); msgs[0].Id != want {
			t.Fatalf("got %v, want %v", msgs[0].Id, want)
		}
	}
}

func TestDelete(t *testing.T) {
	o, err := NewOutputStream("", 5)
	if err != nil {
		t.Fatal(err)
	}

	for i := 1; i < 15; i++ {
		addEmptyMsg(o, uint64(i), 1)
	}

	for i := 1; i < 15; i++ {
		if _, ok := o.Get(robust.Id{Id: uint64(i)}); !ok {
			t.Fatalf("message %d could not be retrieved", i)
		}
	}

	// TODO: verify the file exists on disk

	// Message 5 is an edge condition: it is the last message in the first
	// on-disk file.
	if err := o.Delete(robust.Id{Id: 5}); err != nil {
		t.Fatal(err)
	}

	// TODO: verify the file vanished from disk

	for i := 6; i < 15; i++ {
		if _, ok := o.Get(robust.Id{Id: uint64(i)}); !ok {
			t.Fatalf("message %d could not be retrieved", i)
		}
	}
}

func TestCatchUp(t *testing.T) {
	os, err := NewOutputStream("", 5)
	if err != nil {
		t.Fatal(err)
	}

	addEmptyMsg(os, 1, 1)
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

func TestInterrupt(t *testing.T) {
	os, err := NewOutputStream("", 5)
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
