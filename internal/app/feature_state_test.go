package app

import "testing"

type featureStateTestAlpha struct {
	n int
}

type featureStateTestBeta struct {
	s string
}

// TestFeatureStateSameAppSameType checks that one app asking for one type
// twice gets the same pointer back and the constructor runs exactly once.
func TestFeatureStateSameAppSameType(t *testing.T) {
	a := &app{}
	calls := 0
	newState := func() *featureStateTestAlpha {
		calls++
		return &featureStateTestAlpha{n: calls}
	}
	first := featureState(a, newState)
	second := featureState(a, newState)
	if first != second {
		t.Fatal("two featureState calls for the same app and type returned different pointers")
	}
	if calls != 1 {
		t.Fatalf("constructor ran %d times, want exactly 1", calls)
	}
}

// TestFeatureStateSeparateApps checks that two apps each get their own state
// of one type, so writing through one app's pointer never reaches the other.
func TestFeatureStateSeparateApps(t *testing.T) {
	a := &app{}
	b := &app{}
	newState := func() *featureStateTestAlpha { return &featureStateTestAlpha{} }
	sa := featureState(a, newState)
	sb := featureState(b, newState)
	if sa == sb {
		t.Fatal("two apps share one feature state pointer")
	}
	sa.n = 42
	if sb.n != 0 {
		t.Fatalf("writing one app's state leaked into the other: got %d, want 0", sb.n)
	}
}

// TestFeatureStateSeparateTypes checks that one app holding two types keeps
// them independent: each constructor runs and the values do not interfere.
func TestFeatureStateSeparateTypes(t *testing.T) {
	a := &app{}
	alpha := featureState(a, func() *featureStateTestAlpha { return &featureStateTestAlpha{n: 1} })
	beta := featureState(a, func() *featureStateTestBeta { return &featureStateTestBeta{s: "x"} })
	alpha.n = 7
	beta.s = "y"
	if featureState(a, func() *featureStateTestAlpha { return &featureStateTestAlpha{} }).n != 7 {
		t.Fatal("alpha state was not retained alongside beta")
	}
	if featureState(a, func() *featureStateTestBeta { return &featureStateTestBeta{} }).s != "y" {
		t.Fatal("beta state was not retained alongside alpha")
	}
}

// TestFeatureStateStashUIIsolation checks a real feature end to end: two test
// apps each get their own stash dialog state through stashUI, a change in one
// never appears in the other, and a fresh app still starts with the dialog's
// initial values.
func TestFeatureStateStashUIIsolation(t *testing.T) {
	h1 := newTestApp(t)
	h2 := newTestApp(t)
	s1 := h1.app.stashUI()
	s2 := h2.app.stashUI()
	if s1 == s2 {
		t.Fatal("two test apps share one stash UI state pointer")
	}
	s1.saveUntracked = false
	if !s2.saveUntracked {
		t.Fatal("clearing saveUntracked in one app cleared it in the other")
	}
	h3 := newTestApp(t)
	if !h3.app.stashUI().saveUntracked {
		t.Fatal("a fresh app's stash UI state lost its initial saveUntracked: true")
	}
}
