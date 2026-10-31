package dash

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/histq"
)

// updateEditing is the `/` editor's key map: enter applies the parsed
// query and fetches; esc cancels and keeps the previous query; every other
// key belongs to the textinput.
func (m Model) updateEditing(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		q, err := histq.ParseAt(m.input.Value(), m.now())
		if err != nil {
			// Parse error: the message goes under the input, the previous
			// query and its rows stay, nothing is re-queried.
			m.parseErr = err.Error()
			return m, nil
		}
		m.query = q
		m.text = q.Raw
		m.parseErr = ""
		m.editing = false
		m.input.Blur()
		return m.startFetch()

	case "esc":
		m.editing = false
		m.parseErr = ""
		m.input.Blur()
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}
