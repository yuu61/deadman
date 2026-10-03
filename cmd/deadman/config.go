package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/yuu61/deadman/internal/infrastructure/configfile"
	"github.com/yuu61/deadman/internal/presentation/tui"
)

// processConfig uses a single file snapshot for validation and formatting, before
// any monitoring adapters, logging, host lookups or terminal queries are started.
func processConfig(args cliArgs, output io.Writer) error {
	data, err := os.ReadFile(args.ConfigPath)
	if err != nil {
		return err
	}

	diagnostics, err := configfile.Check(bytes.NewReader(data), tui.CheckSetting)
	if err != nil {
		return fmt.Errorf("%s: read configuration: %w", args.ConfigPath, err)
	}

	var problems []error
	for _, diagnostic := range diagnostics {
		problems = append(
			problems,
			fmt.Errorf("%s:%d: %s", args.ConfigPath, diagnostic.Line, diagnostic.Problem),
		)
	}

	if len(problems) > 0 {
		return errors.Join(problems...)
	}

	if !args.Format {
		return writeConfigOutput(output, args.ConfigPath+": OK\n")
	}

	formatted, err := configfile.Format(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("%s: format configuration: %w", args.ConfigPath, err)
	}

	if args.Write {
		return configfile.Rewrite(args.ConfigPath, data, formatted)
	}

	return writeConfigOutput(output, formatted)
}

func writeConfigOutput(output io.Writer, text string) error {
	_, err := io.WriteString(output, text)
	if err != nil {
		return fmt.Errorf("write configuration output: %w", err)
	}

	return nil
}
