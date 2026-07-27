// Package kafkautil holds small shared Kafka helpers.
package kafkautil

import "strings"

// Brokers splits a comma-separated broker list into a trimmed slice.
func Brokers(csv string) []string {
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
