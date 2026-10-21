package xlsx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Sales":           "sales",
		"Acme / Laptops":  "acme-laptops",
		"  Ünïcode -- 2 ": "n-code-2",
		"***":             "policy",
	} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateCell(t *testing.T) {
	long := strings.Repeat("x", excelize.TotalCellChars+10)
	if got := truncateCell(long); len([]rune(got)) != excelize.TotalCellChars {
		t.Errorf("truncated to %d runes", len([]rune(got)))
	}
	if truncateCell("short") != "short" {
		t.Error("short text changed")
	}
}

func TestWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.xlsx")
	at := time.Date(2026, 3, 4, 12, 20, 30, 0, time.FixedZone("UTC+2", 2*3600))
	rows := [][]any{
		{"a", 1.5, at},
		{"b"}, // short rows are padded with empty cells
	}
	if err := Write(path, []string{"Name", "Count", "When"}, rows, Options{Sheet: "Test"}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, %v", fi.Mode().Perm(), err)
	}
	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	got, _ := f.GetRows("Test")
	if len(got) != 3 || got[0][0] != "Name" || got[2][0] != "b" {
		t.Errorf("rows = %q", got)
	}
	if v, _ := f.GetCellValue("Test", "B2", excelize.Options{RawCellValue: true}); v != "1.5" {
		t.Errorf("number cell = %q", v)
	}
	// The time's own wall clock (12:20:30), not UTC (10:20:30).
	if v, _ := f.GetCellValue("Test", "C2", excelize.Options{RawCellValue: true}); !strings.HasPrefix(v, "46085.514236") {
		t.Errorf("date cell raw value = %q, want 2026-03-04 12:20:30", v)
	}
	if id, _ := f.GetCellStyle("Test", "C2"); id == 0 {
		t.Error("date cell has no date style")
	} else if st, _ := f.GetStyle(id); st.NumFmt != DateTimeFormat {
		t.Errorf("date number format = %d", st.NumFmt)
	}
	id, _ := f.GetCellStyle("Test", "A1")
	st, _ := f.GetStyle(id)
	if st.Font == nil || !st.Font.Bold || len(st.Fill.Color) != 1 || !strings.EqualFold(st.Fill.Color[0], HeaderFill) {
		t.Errorf("header style = font %+v fill %+v", st.Font, st.Fill)
	}
	if p, _ := f.GetPanes("Test"); !p.Freeze || p.YSplit != 1 {
		t.Errorf("panes = %+v", p)
	}
}

func TestFileName(t *testing.T) {
	if got := FileName("fvkeys", "Acme / Sales", time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)); got != "fvkeys-acme-sales-2026-10-21.xlsx" {
		t.Errorf("FileName = %q", got)
	}
}
