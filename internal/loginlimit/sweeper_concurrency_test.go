package loginlimit

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestSweeperTickerEntryConcurrentWithLoginWrites drives the same call the
// ticker goroutine makes against concurrent login writes. Before the fix the
// ticker branch called sweepLocked without holding mu and the process died
// with "fatal error: concurrent map iteration and map write" — an
// unauthenticated remote DoS no HTTP recovery can catch. The fatal abort of
// the test binary IS the failure; the trailing functional check is a
// secondary guard.
func TestSweeperTickerEntryConcurrentWithLoginWrites(t *testing.T) {
	tr := New()
	t.Cleanup(tr.Close)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		// Drive the exact statement the ticker branch executes, without the
		// manual lock the pre-existing test wrongly added around it.
		for i := 0; i < 5000; i++ {
			tr.sweepTick(time.Now().Add(time.Duration(i) * time.Second))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20000; i++ {
			// Wide key spread keeps the sweep's map iteration long enough to
			// overlap concurrent writes; a narrow key space hides the race.
			tr.RecordFailure("login", fmt.Sprintf("user-%d", i), fmt.Sprintf("10.%d.%d.%d", i/65536%256, i/256%256, i%256))
			tr.Check("login", fmt.Sprintf("user-%d", i), "10.0.0.9")
			tr.RecordSuccess("login", fmt.Sprintf("user-%d", i))
		}
	}()
	wg.Wait()

	// The limiter must still function after concurrent sweeping.
	if ok, _ := tr.Check("login", "survivor", "10.9.9.9"); !ok {
		t.Fatal("limiter unusable after concurrent sweep")
	}
}
