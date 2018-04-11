package alertingest

import (
	"testing"

	"github.com/prometheus/common/model"
)

var (
	// cm11, cm12, cm13 are colliding with fp1.
	// cm21, cm22 are colliding with fp2.
	// cm31, cm32 are colliding with fp3, which is below maxMappedFP.
	// Note that fingerprints are set and not actually calculated.
	// The collision detection is independent from the actually used
	// fingerprinting algorithm.
	fp1  = model.Fingerprint(maxMappedFP + 1)
	fp2  = model.Fingerprint(maxMappedFP + 2)
	fp3  = model.Fingerprint(1)
	cm11 = model.Metric{
		"foo":   "bar",
		"dings": "bumms",
	}
	cm12 = model.Metric{
		"bar": "foo",
	}
	cm13 = model.Metric{
		"foo": "bar",
	}
	cm21 = model.Metric{
		"foo":   "bumms",
		"dings": "bar",
	}
	cm22 = model.Metric{
		"dings": "foo",
		"bar":   "bumms",
	}
	cm31 = model.Metric{
		"bumms": "dings",
	}
	cm32 = model.Metric{
		"bumms": "dings",
		"bar":   "foo",
	}
)

func TestFPMapper(t *testing.T) {
	sm := newSeriesMap()

	mapper := newFPMapper(sm)

	// Everything is empty, resolving a FP should do nothing.
	assertFingerprintEqual(t, mapper.mapFP(fp1, cm11), fp1)
	assertFingerprintEqual(t, mapper.mapFP(fp1, cm12), fp1)

	// cm11 is in sm. Adding cm11 should do nothing. Mapping cm12 should resolve
	// the collision.
	sm.put(fp1, &memorySeries{metric: cm11})
	assertFingerprintEqual(t, mapper.mapFP(fp1, cm11), fp1)
	assertFingerprintEqual(t, mapper.mapFP(fp1, cm12), model.Fingerprint(1))

	// The mapped cm12 is added to sm, too. That should not change the outcome.
	sm.put(model.Fingerprint(1), &memorySeries{metric: cm12})
	assertFingerprintEqual(t, mapper.mapFP(fp1, cm11), fp1)
	assertFingerprintEqual(t, mapper.mapFP(fp1, cm12), model.Fingerprint(1))

	// Now map cm13, should reproducibly result in the next mapped FP.
	assertFingerprintEqual(t, mapper.mapFP(fp1, cm13), model.Fingerprint(2))
	assertFingerprintEqual(t, mapper.mapFP(fp1, cm13), model.Fingerprint(2))

	// Add cm13 to sm. Should not change anything.
	sm.put(model.Fingerprint(2), &memorySeries{metric: cm13})
	assertFingerprintEqual(t, mapper.mapFP(fp1, cm11), fp1)
	assertFingerprintEqual(t, mapper.mapFP(fp1, cm12), model.Fingerprint(1))
	assertFingerprintEqual(t, mapper.mapFP(fp1, cm13), model.Fingerprint(2))

	// Now add cm21 and cm22 in the same way, checking the mapped FPs.
	assertFingerprintEqual(t, mapper.mapFP(fp2, cm21), fp2)
	sm.put(fp2, &memorySeries{metric: cm21})
	assertFingerprintEqual(t, mapper.mapFP(fp2, cm21), fp2)
	assertFingerprintEqual(t, mapper.mapFP(fp2, cm22), model.Fingerprint(3))
	sm.put(model.Fingerprint(3), &memorySeries{metric: cm22})
	assertFingerprintEqual(t, mapper.mapFP(fp2, cm21), fp2)
	assertFingerprintEqual(t, mapper.mapFP(fp2, cm22), model.Fingerprint(3))

	// Map cm31, resulting in a mapping straight away.
	assertFingerprintEqual(t, mapper.mapFP(fp3, cm31), model.Fingerprint(4))
	sm.put(model.Fingerprint(4), &memorySeries{metric: cm31})

	// Map cm32, which is now mapped for two reasons...
	assertFingerprintEqual(t, mapper.mapFP(fp3, cm32), model.Fingerprint(5))
	sm.put(model.Fingerprint(5), &memorySeries{metric: cm32})

	// Now check ALL the mappings, just to be sure.
	assertFingerprintEqual(t, mapper.mapFP(fp1, cm11), fp1)
	assertFingerprintEqual(t, mapper.mapFP(fp1, cm12), model.Fingerprint(1))
	assertFingerprintEqual(t, mapper.mapFP(fp1, cm13), model.Fingerprint(2))
	assertFingerprintEqual(t, mapper.mapFP(fp2, cm21), fp2)
	assertFingerprintEqual(t, mapper.mapFP(fp2, cm22), model.Fingerprint(3))
	assertFingerprintEqual(t, mapper.mapFP(fp3, cm31), model.Fingerprint(4))
	assertFingerprintEqual(t, mapper.mapFP(fp3, cm32), model.Fingerprint(5))

	// Remove all the fingerprints from sm, which should change nothing, as
	// the existing mappings stay and should be detected.
	sm.del(fp1)
	sm.del(fp2)
	sm.del(fp3)
	assertFingerprintEqual(t, mapper.mapFP(fp1, cm11), fp1)
	assertFingerprintEqual(t, mapper.mapFP(fp1, cm12), model.Fingerprint(1))
	assertFingerprintEqual(t, mapper.mapFP(fp1, cm13), model.Fingerprint(2))
	assertFingerprintEqual(t, mapper.mapFP(fp2, cm21), fp2)
	assertFingerprintEqual(t, mapper.mapFP(fp2, cm22), model.Fingerprint(3))
	assertFingerprintEqual(t, mapper.mapFP(fp3, cm31), model.Fingerprint(4))
	assertFingerprintEqual(t, mapper.mapFP(fp3, cm32), model.Fingerprint(5))
}

// assertFingerprintEqual asserts that two fingerprints are equal.
func assertFingerprintEqual(t *testing.T, gotFP, wantFP model.Fingerprint) {
	if gotFP != wantFP {
		t.Errorf("got fingerprint %v, want fingerprint %v", gotFP, wantFP)
	}
}