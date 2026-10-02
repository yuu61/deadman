package configfile

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/mattn/go-runewidth"
)

// Format aligns target names, addresses and attributes with spaces, keeps comments
// and blank lines, removes the leading BOM, and emits LF line endings with a final
// newline. Call Check first if invalid settings must be rejected; Format does not
// correct their meaning.
func Format(r io.Reader) (string, error) {
	var lines []formatLine

	err := scanLines(r, func(number int, raw string) {
		lines = append(lines, prepareFormatLine(number, raw))
	})
	if err != nil {
		return "", err
	}

	return alignLines(lines)
}

type formatLine struct {
	body    string
	comment string
	columns []string
}

func prepareFormatLine(number int, raw string) formatLine {
	// Tabs are one byte, so comment offsets also apply to the original text. Keep
	// that text to preserve tabs and all other content inside quotes and comments.
	body := stripComment(strings.ReplaceAll(raw, "\t", " "))
	line := formatLine{
		body:    formatBody(raw[:len(body)]),
		comment: strings.TrimSpace(raw[len(body):]),
	}
	// Collapsing Unicode spaces can create a comment marker that was not one
	// before (e.g. a separator label containing ";　#"). Keep that body intact.
	if stripComment(line.body) != line.body {
		line.body = raw[:len(body)]

		return line
	}

	// A literal BOM in the first name must not become a file marker when its
	// indentation (or the actual file marker before it) is removed.
	if number == 1 && strings.HasPrefix(line.body, "\ufeff") {
		line.body = `""` + line.body
	}

	line.columns = targetColumns(line.body)

	return line
}

// targetColumns splits a normalized target body into its written name, address and
// optional attribute tail. Quoting stays verbatim, including mid-token quotes.
func targetColumns(body string) []string {
	fields, terminated := tokenize(body)
	if !terminated || len(fields) < 2 {
		return nil
	}

	if _, directive := directives[strings.ToLower(fields[0])]; directive ||
		reSeparator.MatchString(fields[0]) {
		return nil
	}

	var columns []string

	start, inQuote := 0, false

	for index, char := range body {
		if char == '"' {
			inQuote = !inQuote
		}

		if char == ' ' && !inQuote {
			columns = append(columns, body[start:index])
			start = index + 1

			if len(columns) == 2 {
				break
			}
		}
	}

	return append(columns, body[start:])
}

func alignLines(lines []formatLine) (string, error) {
	// Full-width characters occupy two cells; combining marks occupy none. Fix
	// ambiguous-width characters at one cell so locale does not change file output.
	width := runewidth.NewCondition()
	width.EastAsianWidth = false
	widths := [2]int{}

	for _, line := range lines {
		for index, column := range line.columns[:min(2, len(line.columns))] {
			widths[index] = max(widths[index], width.StringWidth(column))
		}
	}

	var output strings.Builder

	for index, line := range lines {
		start := output.Len()
		output.WriteString(line.alignedBody(widths, width))

		if line.body != "" && line.comment != "" {
			output.WriteByte(' ')
		}

		output.WriteString(line.comment)
		output.WriteByte('\n')

		// Padding must not produce a file the shared scanner cannot read back.
		if output.Len()-start > bufio.MaxScanTokenSize {
			return "", fmt.Errorf(
				"line %d: aligned line exceeds the configuration line size limit",
				index+1,
			)
		}
	}

	return output.String(), nil
}

func (line formatLine) alignedBody(widths [2]int, width *runewidth.Condition) string {
	if len(line.columns) == 0 {
		return line.body
	}

	var output strings.Builder

	for index, columnWidth := range widths {
		if index >= len(line.columns)-1 {
			break
		}

		column := line.columns[index]
		output.WriteString(column)
		padding := columnWidth - width.StringWidth(column) + 2
		output.WriteString(strings.Repeat(" ", padding))
	}

	output.WriteString(line.columns[len(line.columns)-1])

	return output.String()
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
