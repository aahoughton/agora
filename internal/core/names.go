package core

import "regexp"

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidName reports whether s is a valid namespace, conversation, agent or
// observer name: 1-64 characters of letters, digits, '.', '_' and '-',
// starting with a letter or digit.
func ValidName(s string) bool { return nameRE.MatchString(s) }

func checkName(kind, s string) error {
	if !ValidName(s) {
		return errorf(CodeInvalid, "invalid %s name %q: use 1-64 letters, digits, '.', '_' or '-', starting with a letter or digit", kind, s)
	}
	return nil
}
