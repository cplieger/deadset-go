package inventory

import "math/bits"

// MaxConfigurations is the number of configurations one [ConfigSet] holds. The
// matrix is the set of configurations a project builds, which is the two or three
// a continuous-integration run already covers rather than the product of the
// atoms in its build constraints, so the bound is generous by two orders of
// magnitude; a matrix past it is refused rather than silently narrowed to the
// first sixty-four.
const MaxConfigurations = 64

// ConfigSet is the set of build configurations a declaration exists in: one bit
// per configuration, at the configuration's place in the matrix.
type ConfigSet uint64

// Has reports whether the set holds the configuration at index config.
func (s ConfigSet) Has(config int) bool {
	if config < 0 || config >= MaxConfigurations {
		return false
	}
	return s&(ConfigSet(1)<<config) != 0
}

// Indexes lists the configurations the set holds, in matrix order.
func (s ConfigSet) Indexes() []int {
	found := make([]int, 0, bits.OnesCount64(uint64(s)))
	for config := range MaxConfigurations {
		if s.Has(config) {
			found = append(found, config)
		}
	}
	return found
}

// With returns the set holding config as well. A configuration outside the bound
// is held by no set, which is why the merge refuses a matrix it could not key.
func (s ConfigSet) With(config int) ConfigSet {
	if config < 0 || config >= MaxConfigurations {
		return s
	}
	return s | ConfigSet(1)<<config
}
