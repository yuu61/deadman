package configfile

import (
	"io"
	"strings"
	"unicode"
)

// Format collapses whitespace outside quoted spans, keeps comments and blank lines,
// removes the leading BOM, and emits LF line endings with a final newline. Call Check
// first if invalid settings must be rejected; Format does not correct their meaning.
func Format(r io.Reader) (string, error) {
	var output strings.Builder

	err := scanLines(r, func(number int, raw string) {
		// Tabs are one byte, so comment offsets also apply to the original text. Keep
		// that text to preserve tabs and all other content inside quotes and comments.
		body := stripComment(strings.ReplaceAll(raw, "\t", " "))
		comment := strings.TrimSpace(raw[len(body):])
		formatted := formatBody(raw[:len(body)])
		// Collapsing Unicode spaces can create a comment marker that was not one
		// before (e.g. a separator label containing ";　#"). Keep that body intact.
		if stripComment(formatted) != formatted {
			formatted = raw[:len(body)]
		}

		// A literal BOM in the first name must not become a file marker when its
		// indentation (or the actual file marker before it) is removed.
		if number == 1 && strings.HasPrefix(formatted, "\ufeff") {
			formatted = `""` + formatted
		}

		output.WriteString(formatted)

		if formatted != "" && comment != "" {
			output.WriteByte(' ')
		}

		output.WriteString(comment)
		output.WriteByte('\n')
	})
	if err != nil {
		return "", err
	}

	return output.String(), nil
}

func formatBody(body string) string {
	var output strings.Builder

	inQuote, space := false, false
	for _, char := range body {
		if !inQuote && unicode.IsSpace(char) {
			space = output.Len() > 0

			continue
		}

		if space {
			output.WriteByte(' ')

			space = false
		}

		output.WriteRune(char)

		if char == '"' {
			inQuote = !inQuote
		}
	}

	return output.String()
}
