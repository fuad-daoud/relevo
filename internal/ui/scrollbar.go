package ui

import "github.com/fuad-daoud/relevo/internal/ui/widget"

func barCells(total, height, offset int) []string {
	if height <= 0 || total <= height {
		return nil
	}
	size := height * height / total
	if size < 1 {
		size = 1
	}
	top := offset * (height - size) / (total - height)
	cells := make([]string, height)
	for i := range cells {
		if i >= top && i < top+size {
			cells[i] = widget.MutedStyle.Render("█")
			continue
		}
		cells[i] = widget.BorderStyle.Render("│")
	}
	return cells
}
