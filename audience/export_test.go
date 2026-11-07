package audience

import "time"

// SetClock replaces the clock of the resolver (tests only).
func SetClock(r *Resolver, now func() time.Time) { r.now = now }

// SetExistsTimeout replaces the timeout of CheckExists (tests only).
func SetExistsTimeout(r *Resolver, d time.Duration) { r.existsTimeout = d }
