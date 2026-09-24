package aimcp

import "strings"

func validDisplayText(value string, maximum int) bool {
	if len([]byte(value)) > maximum {
		return false
	}
	for _, r := range value {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}
func validText(value string, maximum int) bool {
	if len([]byte(value)) > maximum || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	for _, r := range value {
		if r < 0x20 && r != '\t' {
			return false
		}
	}
	return true
}
