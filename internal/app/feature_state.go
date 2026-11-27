package app

import "reflect"

// featureState returns a's state of type T, made by newState on first use.
// Feature files keep their UI state here instead of adding fields to app,
// so a new feature does not edit app.go. The state lives as long as a.
// UI thread only, like every other app field.
func featureState[T any](a *app, newState func() *T) *T {
	if a.featureStates == nil {
		a.featureStates = map[reflect.Type]any{}
	}
	key := reflect.TypeFor[T]()
	if st, ok := a.featureStates[key]; ok {
		return st.(*T)
	}
	st := newState()
	a.featureStates[key] = st
	return st
}
