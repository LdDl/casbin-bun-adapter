package casbinbunadapter

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

func validatePolicyType(sec, ptype string) error {
	if ptype == "" || !utf8.ValidString(ptype) || strings.ContainsRune(ptype, '\x00') {
		return fmt.Errorf("policy type must be a non-empty UTF-8 string without NUL")
	}
	if ptype[0] != 'p' && ptype[0] != 'g' {
		return fmt.Errorf("policy type must start with p or g")
	}
	if sec != "" && sec != ptype[:1] {
		return fmt.Errorf("policy section does not match policy type")
	}
	return nil
}

func validatePolicy(sec, ptype string, rule []string) error {
	err := validatePolicyType(sec, ptype)
	if err != nil {
		return err
	}
	if sec != "p" && sec != "g" {
		return fmt.Errorf("policy section must be p or g")
	}
	if len(rule) == 0 || len(rule) > 6 {
		return fmt.Errorf("policy rule must contain between one and six fields")
	}
	return validatePolicyValues(rule)
}

func validatePolicyValues(values []string) error {
	for i, value := range values {
		if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("policy field %d must be valid UTF-8 without NUL", i)
		}
	}
	return nil
}
