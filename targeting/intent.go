package targeting

import "strconv"

// Intent is an audience intent score in [0, 1] stored alongside a user's
// membership in a package audience. Higher values indicate stronger targeting signal.
type Intent float64

// String serialises the intent score for storage in a hash field.
func (i Intent) String() string {
	return strconv.FormatFloat(float64(i), 'f', -1, 64)
}

// ParseIntent deserialises an intent score from its stored string form.
func ParseIntent(s string) (Intent, error) {
	f, err := strconv.ParseFloat(s, 64)
	return Intent(f), err
}
