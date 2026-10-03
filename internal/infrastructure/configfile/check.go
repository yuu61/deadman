package configfile

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/yuu61/deadman/internal/application/config"
	"github.com/yuu61/deadman/internal/domain/probe"
)

// Diagnostic describes a configuration problem at a physical, one-based line number.
type Diagnostic struct {
	Line    int
	Problem string
}

// Check reports all invalid lines, including constructs that monitoring tolerates.
// It uses the normal parser and probe.Compile, without creating probes or performing
// network I/O. checkSetting must validate the frontend's display vocabulary and ranges.
func Check(r io.Reader, checkSetting func(config.Setting) error) ([]Diagnostic, error) {
	var diagnostics []Diagnostic

	err := scanLines(r, func(number int, raw string) {
		line := stripComment(strings.ReplaceAll(raw, "\t", " "))
		for _, problem := range checkLine(line, checkSetting) {
			diagnostics = append(diagnostics, Diagnostic{Line: number, Problem: problem.Error()})
		}
	})

	return diagnostics, err
}

func checkLine(line string, checkSetting func(config.Setting) error) []error {
	fields, terminated := tokenize(line)
	if !terminated {
		return []error{errors.New("unterminated double quote")}
	}

	if len(fields) == 0 {
		return nil
	}

	keyword := strings.ToLower(fields[0])
	if _, ok := directives[keyword]; ok {
		return checkDirective(keyword, fields[1:], checkSetting)
	}

	return checkTarget(fields)
}

func checkTarget(fields []string) []error {
	line, note := parseTarget(fields)

	var problems []error
	if len(note.Dropped) > 0 {
		problems = append(
			problems,
			errors.New("unexpected bare words after address; expected key=value attributes"),
		)
	}

	switch value := line.(type) {
	case config.Target:
		_, err := probe.Compile(value.ProbeSpec())
		if err != nil {
			problems = append(problems, err)
		}
	case config.Malformed:
		problems = append(problems, errors.New(value.Problem))
	case config.Separator:
		// Separator labels have no probe parameters to validate.
	default:
		panic("unhandled config line")
	}

	return problems
}

func checkDirective(
	keyword string,
	args []string,
	checkSetting func(config.Setting) error,
) []error {
	if len(args) == 0 {
		return []error{fmt.Errorf("%s needs a value", keyword)}
	}

	if keyword == columnDirective {
		return checkColumns(args, checkSetting)
	}

	if len(args) != 1 {
		return []error{fmt.Errorf("%s expects exactly one value", keyword)}
	}

	setting, err := readSetting(keyword, args[0])
	if err == nil {
		err = checkSetting(setting)
	}

	if err != nil {
		return []error{err}
	}

	return nil
}

func readSetting(keyword, value string) (config.Setting, error) {
	switch keyword {
	case scaleDirective:
		number, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("scale expects a number: %w", err)
		}

		return config.Scale(number), nil
	case splitDirective:
		number, err := strconv.Atoi(value)
		if err != nil {
			return nil, fmt.Errorf("split expects an integer: %w", err)
		}

		return config.Split(number), nil
	case precisionDirective:
		return config.Precision(value), nil
	case glyphDirective:
		return config.Glyph(value), nil
	default:
		panic("unhandled display directive")
	}
}

func checkColumns(args []string, checkSetting func(config.Setting) error) []error {
	var problems []error

	for _, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		if !ok || key == "" {
			problems = append(problems, errors.New("columns expects KEY=on|off attributes"))

			continue
		}

		visible, err := parseBool(value)
		if err != nil {
			problems = append(problems, fmt.Errorf("columns %s: %w", key, err))

			continue
		}

		err = checkSetting(config.Column{Key: strings.ToUpper(key), Visible: visible})
		if err != nil {
			problems = append(problems, err)
		}
	}

	return problems
}
