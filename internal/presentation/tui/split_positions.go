package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/yuu61/deadman/internal/application/monitoring"
)

// splitSection contains absolute table indices between explicit column breaks.
// A break with a description is a display row; an unlabelled break uses no space.
// A laid-out column uses the same type for the indices it shows from top to bottom.
type splitSection struct {
	indices []int
}

// splitGrid is the one row of columns that holds every section; the shared
// scroll window moves over its tallest column.
type splitGrid struct {
	columns []splitSection
	rows    int
}

func (m Model) refreshSections() Model {
	var sections []splitSection

	current := splitSection{}
	explicit := false

	for index, line := range m.lines {
		boundary, ok := line.(monitoring.ColumnBreak)
		if !ok {
			current.indices = append(current.indices, index)

			continue
		}

		if explicit || len(current.indices) > 0 {
			sections = append(sections, current)
		}

		current = splitSection{}
		if boundary.Label != "" {
			current.indices = append(current.indices, index)
		}

		explicit = true
	}

	if explicit {
		sections = append(sections, current)
	}

	m.sections = sections

	return m
}

func (m Model) splitGrid() splitGrid {
	grid := splitGrid{columns: m.splitColumns(m.effectiveCols())}
	for _, column := range grid.columns {
		grid.rows = max(grid.rows, len(column.indices))
	}

	return grid
}

// splitCapacity counts the columns the content can fill. A guide provides a
// preferred break; it must not cap the number of columns requested by the user.
func (m Model) splitCapacity() int {
	capacity := 0
	for _, section := range m.sections {
		capacity += max(1, len(section.indices))
	}

	return capacity
}

// splitColumns lays the sections out side by side in cols columns. With enough
// columns every explicit boundary starts a column and the longest sections are
// divided to fill the rest; with fewer, whole sections share columns.
func (m Model) splitColumns(cols int) []splitSection {
	if cols < len(m.sections) {
		return stackSections(m.sections, cols)
	}

	parts := make([]int, len(m.sections))
	for index := range parts {
		parts[index] = 1
	}

	for extra := cols - len(parts); extra > 0; extra-- {
		index := m.longestSplit(parts)
		parts[index]++
	}

	columns := make([]splitSection, 0, cols)
	for index, section := range m.sections {
		columns = append(columns, partitionSection(section, parts[index])...)
	}

	return columns
}

// stackSections fits more sections than columns without dividing a section or
// pushing one below the screen: consecutive sections share a column, and the
// tallest column is as short as any such arrangement allows.
func stackSections(sections []splitSection, cols int) []splitSection {
	tallest, total := 0, 0
	for _, section := range sections {
		tallest = max(tallest, len(section.indices))
		total += len(section.indices)
	}

	height := tallest + sort.Search(total-tallest, func(extra int) bool {
		return stackedColumns(sections, tallest+extra) <= cols
	})

	columns := make([]splitSection, 0, cols)
	next := 0

	for column := range cols {
		var stacked []int

		// Leave at least one section for each later column, so none is header-only.
		for first := next; next < len(sections) && len(sections)-next > cols-column-1; next++ {
			size := len(sections[next].indices)
			if next > first && len(stacked)+size > height {
				break
			}

			stacked = append(stacked, sections[next].indices...)
		}

		columns = append(columns, splitSection{indices: stacked})
	}

	return columns
}

// stackedColumns counts the columns needed when each is filled up to height.
func stackedColumns(sections []splitSection, height int) int {
	columns, used := 1, 0
	for _, section := range sections {
		if used > 0 && used+len(section.indices) > height {
			columns, used = columns+1, 0
		}

		used += len(section.indices)
	}

	return columns
}

func (m Model) longestSplit(parts []int) int {
	longest, height := 0, 0

	for index, section := range m.sections {
		if len(section.indices) > parts[index] {
			if candidate := ceilDiv(len(section.indices), parts[index]); candidate > height {
				longest, height = index, candidate
			}
		}
	}

	return longest
}

func partitionSection(section splitSection, parts int) []splitSection {
	columns := make([]splitSection, 0, parts)
	start := 0

	for index := range parts {
		size := len(section.indices) / parts
		if index < len(section.indices)%parts {
			size++
		}

		columns = append(columns, splitSection{indices: section.indices[start : start+size]})
		start += size
	}

	return columns
}

func (m Model) splitViewport() viewport {
	grid := m.splitGrid()
	cols := m.effectiveCols()

	usable := m.usableRows()
	if m.width <= 0 || m.height <= 0 {
		usable = grid.rows
	}

	status := grid.rows > usable && usable >= 2
	if status {
		usable--
	}

	count := min(grid.rows, usable)
	maxTop := grid.rows - count

	return viewport{
		top:    max(0, min(m.scrollTop, maxTop)),
		count:  count,
		maxTop: maxTop,
		cols:   cols,
		perCol: count,
		active: maxTop > 0,
		status: status,
	}
}

func (m Model) renderSplitPositions(vp viewport) string {
	width := m.colContentWidth()
	grid := m.splitGrid()
	blocks := make([][]string, vp.cols)

	for column := range blocks {
		lines := make([]string, 0, 1+vp.count)

		lines = append(lines, padCell(m.headerLine(), width))
		for row := range vp.count {
			cell := m.splitCell(grid, vp.top+row, column, width)
			lines = append(lines, padCell(cell, width))
		}

		blocks[column] = lines
	}

	return joinColumns(blocks)
}

func (m Model) splitCell(grid splitGrid, row, column, width int) string {
	if column < len(grid.columns) && row < len(grid.columns[column].indices) {
		return m.rowLine(grid.columns[column].indices[row], width)
	}

	return ""
}

func (m Model) splitScrollStatus(vp viewport) string {
	text := fmt.Sprintf("%s[%d-%d/%d] j/k scroll  PgUp/PgDn page  g/G top/bottom", rear,
		vp.top+1, vp.top+vp.count, m.splitGrid().rows)

	return styleBold.Render(strings.TrimRight(padCell(text, m.width), " "))
}
