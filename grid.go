package main

import (
	"archive/zip"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/extrame/xls"
)

// sheetGrid is a dense text grid of one worksheet.
type sheetGrid struct {
	Name string
	Rows [][]string
}

func gridCell(g [][]string, r, c int) string {
	if r < 0 || r >= len(g) || c < 0 || c >= len(g[r]) {
		return ""
	}
	return strings.TrimSpace(g[r][c])
}

// readGrids reads every sheet of an .xlsx or .xls file into dense text grids.
func readGrids(path string) (out []sheetGrid, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("تعذّرت قراءة الملف: %v", r)
		}
	}()
	low := strings.ToLower(path)
	if strings.HasSuffix(low, ".xls") {
		wb, e := xls.Open(path, "utf-8")
		if e != nil {
			return nil, e
		}
		for i := 0; i < wb.NumSheets(); i++ {
			sh := wb.GetSheet(i)
			if sh == nil {
				continue
			}
			g := sheetGrid{Name: sh.Name}
			for r := 0; r <= int(sh.MaxRow); r++ {
				var cells []string
				func() {
					defer func() { recover() }()
					row := sh.Row(r)
					if row == nil {
						return
					}
					last := row.LastCol()
					for c := 0; c <= last && c < 80; c++ {
						cells = append(cells, strings.TrimSpace(row.Col(c)))
					}
				}()
				g.Rows = append(g.Rows, cells)
			}
			out = append(out, g)
		}
		return out, nil
	}
	zr, e := zip.OpenReader(path)
	if e != nil {
		return nil, e
	}
	defer zr.Close()
	shared := xlsxShared(zr)
	for _, sh := range xlsxSheets(zr) {
		g := sheetGrid{Name: sh.Name}
		for _, row := range xlsxRows(zr, sh.File, shared) {
			if len(row) == 0 {
				continue
			}
			rIdx := 0
			if _, rn := splitRef(row[0].Ref); rn > 0 {
				rIdx = rn - 1
			}
			for len(g.Rows) <= rIdx {
				g.Rows = append(g.Rows, nil)
			}
			for _, c := range row {
				ci, _ := splitRef(c.Ref)
				for len(g.Rows[rIdx]) <= ci {
					g.Rows[rIdx] = append(g.Rows[rIdx], "")
				}
				g.Rows[rIdx][ci] = c.Value
			}
		}
		out = append(out, g)
	}
	return out, nil
}

// excelSerialDate converts a serial like "45870" to 2025-08-01 ("" when it is not a plausible date).
func excelSerialDate(s string) string {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 20000 || f > 80000 {
		return ""
	}
	return time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(f)).Format("2006-01-02")
}
