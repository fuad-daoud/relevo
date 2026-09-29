package ui

// barCells is the round pane's one-cell scrollbar: one cell per viewport row,
// the track on every row with the thumb over the visible fraction of the
// content. It is pure geometry over the viewport's own numbers after view()
// set width and height: the total logical lines, the height and the offset.
// nil means hidden -- the content fits, the viewport holds no lines, or it
// has no height.
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
			cells[i] = mutedStyle.Render("█")
			continue
		}
		cells[i] = borderStyle.Render("│")
	}
	return cells
}
