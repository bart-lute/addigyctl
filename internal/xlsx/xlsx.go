// Package xlsx writes a table to an Excel file in the style addigyctl uses for
// exports: a bold, light blue, frozen and filterable header row, columns sized
// to their contents, and real Excel numbers and dates.
package xlsx

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"

	"github.com/ginkio/addigyctl/internal/output"
)

const (
	// HeaderFill is the background of the header row (a light blue).
	HeaderFill = "DDEBF7"

	// DateTimeFormat is Excel's built-in date-and-time format (number
	// format 22). Excel shows it in the viewer's own regional format.
	DateTimeFormat = 22

	// minWidth and maxWidth bound the column widths, in characters.
	minWidth = 8
	maxWidth = 50
)

// Options configures Build and Write.
type Options struct {
	// Sheet is the name of the worksheet.
	Sheet string
	// DateFormat is the Excel number format for timestamps, such as
	// "dd-mm-yyyy hh:mm" (the tokens of the config file's "date_format" mean
	// the same in Excel). Empty means DateTimeFormat.
	DateFormat string
}

// Build puts headers and rows on a new workbook. A cell value is written as
// is: a float64 as a number, a string as text (cut to what a cell can hold),
// nil as an empty cell, and a time.Time as an Excel date showing the time's
// own wall clock (Excel has no time zones, so convert it first).
func Build(headers []string, rows [][]any, o Options) (*excelize.File, error) {
	f := excelize.NewFile()
	if err := f.SetSheetName("Sheet1", o.Sheet); err != nil {
		f.Close()
		return nil, err
	}
	if err := fill(f, headers, rows, o); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func fill(f *excelize.File, headers []string, rows [][]any, o Options) error {
	sheet := o.Sheet
	headerStyle, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true},
		Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{HeaderFill}},
	})
	if err != nil {
		return err
	}
	dateStyle := &excelize.Style{NumFmt: DateTimeFormat}
	if o.DateFormat != "" {
		dateStyle = &excelize.Style{CustomNumFmt: &o.DateFormat}
	}
	dateStyleID, err := f.NewStyle(dateStyle)
	if err != nil {
		return err
	}

	widths := make([]int, len(headers))
	isDate := make([]bool, len(headers))
	header := make([]any, len(headers))
	for i, h := range headers {
		header[i] = h
		widths[i] = utf8.RuneCountInString(h)
	}
	if err := f.SetSheetRow(sheet, "A1", &header); err != nil {
		return err
	}
	cells := make([]any, len(headers))
	for r, row := range rows {
		for i := range cells {
			var v any
			if i < len(row) {
				v = row[i]
			}
			switch x := v.(type) {
			case string:
				v = truncateCell(x)
				widths[i] = max(widths[i], utf8.RuneCountInString(x))
			case float64:
				widths[i] = max(widths[i], len(output.Field(x)))
			case time.Time:
				v = time.Date(x.Year(), x.Month(), x.Day(), x.Hour(), x.Minute(), x.Second(), 0, time.UTC)
				isDate[i] = true
				widths[i] = max(widths[i], 19)
			}
			cells[i] = v
		}
		cell, err := excelize.CoordinatesToCellName(1, r+2)
		if err != nil {
			return err
		}
		if err := f.SetSheetRow(sheet, cell, &cells); err != nil {
			return err
		}
	}

	lastRow := len(rows) + 1
	for i := range headers {
		name, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			return err
		}
		width := float64(min(max(widths[i], minWidth), maxWidth) + 2)
		if err := f.SetColWidth(sheet, name, name, width); err != nil {
			return err
		}
		if isDate[i] {
			if err := f.SetCellStyle(sheet, name+"2", fmt.Sprintf("%s%d", name, lastRow), dateStyleID); err != nil {
				return err
			}
		}
	}
	last, err := excelize.CoordinatesToCellName(len(headers), 1)
	if err != nil {
		return err
	}
	if err := f.SetCellStyle(sheet, "A1", last, headerStyle); err != nil {
		return err
	}
	if err := f.SetPanes(sheet, &excelize.Panes{
		Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft",
	}); err != nil {
		return err
	}
	lastCell, err := excelize.CoordinatesToCellName(len(headers), lastRow)
	if err != nil {
		return err
	}
	return f.AutoFilter(sheet, "A1:"+lastCell, nil)
}

// Write builds the workbook and saves it to path, readable by the current
// user only: an export can hold personal data or secrets. An existing file
// is overwritten.
func Write(path string, headers []string, rows [][]any, o Options) error {
	f, err := Build(headers, rows, o)
	if err != nil {
		return err
	}
	defer f.Close()
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := f.Write(out); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// truncateCell shortens text to what an Excel cell can hold.
func truncateCell(s string) string {
	if utf8.RuneCountInString(s) <= excelize.TotalCellChars {
		return s
	}
	return output.Truncate(s, excelize.TotalCellChars)
}

// FileName returns "<prefix>-<scope>-<yyyy-mm-dd>.xlsx", with scope (a
// policy name, say) made safe for a file name.
func FileName(prefix, scope string, day time.Time) string {
	return fmt.Sprintf("%s-%s-%s.xlsx", prefix, slug(scope), day.Format("2006-01-02"))
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	if s := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-"); s != "" {
		return s
	}
	return "policy"
}
