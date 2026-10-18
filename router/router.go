// Package router maps a prompt to a driver: @name tags in the text or the
// session's ACP mode.
package router

import "strings"

// ParsePrompt extracts a leading @driver tag. "@pi fix the bug" -> ("pi", "fix the bug").
func ParsePrompt(text string) (tag, clean string) {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "@") {
		return "", text
	}
	fields := strings.Fields(t)
	name := strings.TrimPrefix(fields[0], "@")
	return name, strings.TrimSpace(strings.TrimPrefix(t, fields[0]))
}
