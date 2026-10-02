package configfile

import (
	"cmp"
	"slices"
	"strings"
)

// attributeOrder groups method, routing, method-specific options and credentials.
// Missing attributes remain missing; the formatter changes only written order.
const attributeOrder = "probe relay nexthop source resolve_family os port scheme alpn sni verify " +
	"user key username password community"

// writtenFields splits a normalized body at spaces outside quotes, preserving the
// exact spelling of each field. tokenize supplies its unquoted counterpart.
func writtenFields(body string) []string {
	var fields []string

	start, inQuote := 0, false

	for index, char := range body {
		if char == '"' {
			inQuote = !inQuote
		}

		if char == ' ' && !inQuote {
			fields = append(fields, body[start:index])
			start = index + 1
		}
	}

	return append(fields, body[start:])
}

func orderedAttributes(written, decoded []string) string {
	_, dropped, problem := parseAttributes(decoded)
	if problem != "" || len(dropped) > 0 {
		// Invalid syntax can have order-sensitive diagnostics. The CLI rejects it;
		// direct Format callers still keep the same problem and parser notes.
		return strings.Join(written, " ")
	}

	order := strings.Fields(attributeOrder)

	slices.SortStableFunc(written, func(left, right string) int {
		leftKey := writtenAttributeKey(left)
		rightKey := writtenAttributeKey(right)

		comparison := cmp.Compare(attributeRank(order, leftKey), attributeRank(order, rightKey))
		if comparison != 0 {
			return comparison
		}

		// New or unknown attributes follow known ones in alphabetical order.
		return strings.Compare(leftKey, rightKey)
	})

	return strings.Join(written, " ")
}

func writtenAttributeKey(field string) string {
	unquoted := strings.ReplaceAll(strings.ReplaceAll(field, "\t", " "), `"`, "")
	key, _, _ := strings.Cut(unquoted, "=")

	return key
}

func attributeRank(order []string, key string) int {
	for rank, name := range order {
		if key == name {
			return rank
		}
	}

	return len(order)
}
