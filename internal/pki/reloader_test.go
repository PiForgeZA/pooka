package pki

import (
	"crypto/tls"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestReloaderCallsLoadAtMostOncePerInterval(t *testing.T) {
	now := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	calls := 0
	r := &Reloader{
		Load: func(at time.Time) (tls.Certificate, error) {
			calls++
			return tls.Certificate{Certificate: [][]byte{[]byte(at.Format(time.RFC3339))}}, nil
		},
		Interval: 24 * time.Hour,
		Now:      func() time.Time { return now },
	}
	first, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	now = now.Add(23 * time.Hour)
	again, _ := r.GetCertificate(nil)
	if calls != 1 || again != first {
		t.Fatalf("within the interval: calls = %d, same = %v", calls, again == first)
	}
	now = now.Add(2 * time.Hour)
	later, _ := r.GetCertificate(nil)
	if calls != 2 || later == first {
		t.Fatalf("after the interval: calls = %d, same = %v", calls, later == first)
	}
	if got := string(later.Certificate[0]); got != now.Format(time.RFC3339) {
		t.Errorf("Load got time %s, want %s", got, now.Format(time.RFC3339))
	}
}

func TestReloaderKeepsTheOldPairWhenLoadFails(t *testing.T) {
	now := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	fail := false
	logged := ""
	r := &Reloader{
		Load: func(time.Time) (tls.Certificate, error) {
			if fail {
				return tls.Certificate{}, errors.New("disk is gone")
			}
			return tls.Certificate{Certificate: [][]byte{[]byte("good")}}, nil
		},
		Interval: time.Hour,
		Now:      func() time.Time { return now },
		Logf:     func(format string, args ...any) { logged = format },
	}
	first, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	fail = true
	now = now.Add(2 * time.Hour)
	got, err := r.GetCertificate(nil)
	if err != nil || got != first {
		t.Fatalf("got %v, err %v; want the cached pair", got, err)
	}
	if logged == "" {
		t.Error("the failure was not logged")
	}
	// The next call within the interval does not retry.
	fail = false
	logged = ""
	if _, _ = r.GetCertificate(nil); logged != "" {
		t.Error("retried within the interval")
	}
}

func TestReloaderFailsWhenNothingWasEverLoaded(t *testing.T) {
	r := &Reloader{Load: func(time.Time) (tls.Certificate, error) { return tls.Certificate{}, errors.New("no") }}
	if _, err := r.GetCertificate(nil); err == nil {
		t.Error("want the load error when there is no cached pair")
	}
}

func TestReloaderIsSafeForConcurrentUse(t *testing.T) {
	calls := 0
	r := &Reloader{
		Load: func(time.Time) (tls.Certificate, error) {
			calls++
			return tls.Certificate{Certificate: [][]byte{[]byte("x")}}, nil
		},
		Interval: time.Hour,
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.GetCertificate(nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}
