// Package phone brings the numbers of an uploaded sheet to one form.
package phone

import "strings"

// Normalize reads a phone number as people write it in a sheet ("90 123-45-67",
// "998901234567", "+998 (90) 123 45 67", "8 90 1234567") and answers it as
// "+998901234567". Numbers of other countries pass if written in full with a
// "+". ok is false for anything that is not a phone number.
func Normalize(s string) (string, bool) {
	s = strings.TrimSpace(s)
	plus := strings.HasPrefix(s, "+")
	// a sheet that kept the number as a float: "998901234567.0"
	if i := strings.IndexByte(s, '.'); i > 0 && strings.Trim(s[i+1:], "0") == "" {
		s = s[:i]
	}
	var d strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			d.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')' || r == '+' || r == '.' || r == ' ':
		default:
			return "", false
		}
	}
	digits := d.String()
	switch {
	case len(digits) == 12 && strings.HasPrefix(digits, "998"):
		return "+" + digits, true
	case len(digits) == 9 && !plus:
		return "+998" + digits, true
	case len(digits) == 10 && digits[0] == '8' && !plus: // the old trunk prefix
		return "+998" + digits[1:], true
	case plus && len(digits) >= 10 && len(digits) <= 15:
		return "+" + digits, true
	}
	return "", false
}

// Mask hides the middle of a number for logs and lists: "+998 90 *** ** 67".
func Mask(e164 string) string {
	if len(e164) < 9 {
		return "***"
	}
	return e164[:len(e164)-7] + "*****" + e164[len(e164)-2:]
}
