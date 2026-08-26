package nosharing

import "testing"

func TestFactsEnabledNilFactTypes(t *testing.T) {
	oldTypes := Analyzer.FactTypes
	oldOff := factsOff
	Analyzer.FactTypes = nil
	factsOff = true
	t.Cleanup(func() {
		Analyzer.FactTypes = oldTypes
		factsOff = oldOff
	})
	if factsEnabled() {
		t.Fatal("factsEnabled with nil FactTypes")
	}
	a := &analyzer{}
	a.exportObjectFact(nil, &MayShareParams{})
	a.exportPackageFact(&HotGlobals{})
}
