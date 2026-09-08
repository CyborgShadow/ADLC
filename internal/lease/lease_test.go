package lease

import (
	"testing"
	"time"
)

func store(t *testing.T) *Store {
	t.Helper()
	s := New(t.TempDir(), 180, nil)
	s.Now = func() time.Time { return time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC) }
	return s
}

// TestEverySpellingOfOneIdIsOneClaim pins the canonicalisation.
//
// The duplication was total — same diagnosis, same fix, two runs of capacity
// spent on one item.
func TestEverySpellingOfOneIdIsOneClaim(t *testing.T) {
	s := store(t)
	if out, err := s.Acquire(Lease{Key: "S1-001", RunID: "a"}); err != nil || !out.Granted {
		t.Fatalf("first claim should be granted: %v %+v", err, out)
	}
	for _, spelling := range []string{"s1-001", "S1_001", " S1-001 ", "S1--001"} {
		out, err := s.Acquire(Lease{Key: spelling, RunID: "b"})
		if err != nil {
			t.Fatal(err)
		}
		if out.Granted {
			t.Errorf("%q should collapse to the same claim as S1-001 and be refused", spelling)
		}
	}
	// Clean case: a genuinely different item is still free.
	if out, _ := s.Acquire(Lease{Key: "S1-002", RunID: "c"}); !out.Granted {
		t.Error("an unrelated item must still be claimable; a collision check that refuses everything is not a check")
	}
}

// Two agents editing one file conflict at merge; two agents changing one
// machine cause an outage.
func TestLeasingAFleetLocksItsHosts(t *testing.T) {
	s := store(t)
	if out, _ := s.Acquire(Lease{Key: "S1-001", RunID: "a", Resources: []string{"fleet:prod"}}); !out.Granted {
		t.Fatal("first claim should be granted")
	}
	out, err := s.Acquire(Lease{Key: "S1-002", RunID: "b", Resources: []string{"fleet:prod/host:web-01"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Granted {
		t.Fatal("a host inside a leased fleet must not be separately claimable")
	}
	if out.Reason != "resource" {
		t.Errorf("want a resource conflict, got %q", out.Reason)
	}
	// And the other direction: holding a host blocks a fleet-wide sweep.
	s2 := store(t)
	s2.Acquire(Lease{Key: "S1-001", RunID: "a", Resources: []string{"fleet:prod/host:web-01"}})
	if out, _ := s2.Acquire(Lease{Key: "S1-002", RunID: "b", Resources: []string{"fleet:prod"}}); out.Granted {
		t.Fatal("a fleet-wide change must not run under a held host")
	}
	// Clean case: a different fleet is unaffected.
	if out, _ := s.Acquire(Lease{Key: "S1-003", RunID: "c", Resources: []string{"fleet:staging"}}); !out.Granted {
		t.Error("an unrelated fleet must still be claimable")
	}
}

func TestAnExpiredLeaseIsSweptRatherThanHonoured(t *testing.T) {
	s := store(t)
	base := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return base }
	s.Acquire(Lease{Key: "S1-001", RunID: "crashed", TTLMinutes: 30})

	s.Now = func() time.Time { return base.Add(31 * time.Minute) }
	out, err := s.Acquire(Lease{Key: "S1-001", RunID: "next"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Granted {
		t.Fatal("a crashed run must not hold a resource forever")
	}
	live, _ := s.Live()
	if len(live) != 1 || live[0].RunID != "next" {
		t.Fatalf("the expired lease should have been swept, got %+v", live)
	}
}

func TestASubjectOverlapIsAdvisedNotRefused(t *testing.T) {
	s := store(t)
	s.Acquire(Lease{Key: "S1-001", RunID: "a", Subject: "harden the bastion sshd configuration"})
	out, err := s.Acquire(Lease{Key: "S1-002", RunID: "b", Subject: "rotate the bastion certificate"})
	if err != nil {
		t.Fatal(err)
	}
	// Biased toward refusing on resources, but a word shared between two free
	// text descriptions is reported.
	if !out.Granted {
		t.Fatal("a shared descriptive word must not refuse a claim")
	}
	if out.Advisory == "" {
		t.Fatal("a shared distinctive word should still be surfaced")
	}
}

func TestALeaseWithNoRunIdIsRefused(t *testing.T) {
	s := store(t)
	if _, err := s.Acquire(Lease{Key: "S1-001"}); err == nil {
		t.Fatal("a lease with no run id is invisible to every guard that keys on the run")
	}
	if _, err := s.Acquire(Lease{RunID: "a"}); err == nil {
		t.Fatal("a lease with no key is not a claim on anything")
	}
}

func TestReleasingUnderADifferentSpellingStillReleases(t *testing.T) {
	s := store(t)
	s.Acquire(Lease{Key: "S1-001", RunID: "a"})
	if err := s.Release("s1_001", "a"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if live, _ := s.Live(); len(live) != 0 {
		t.Fatalf("a lease taken under one spelling and released under another must not leak, got %+v", live)
	}
}

func TestOneRunMayRestateItsOwnClaim(t *testing.T) {
	s := store(t)
	s.Acquire(Lease{Key: "S1-001", RunID: "a"})
	out, err := s.Acquire(Lease{Key: "S1-001", RunID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Reason == "held" {
		t.Fatal("a run must not be blocked by its own live claim")
	}
}
