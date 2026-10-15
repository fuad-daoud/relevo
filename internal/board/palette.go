// palette.go holds the palette type's names in the board package, which the
// board's own request types, the server and the page contract all spell the
// old way. The type and the two appState defaults are the theme package's;
// the stored sets, the palettes' seed file and the lookup live there
// (internal/board/theme).
package board

import "github.com/fuad-daoud/relevo/internal/board/theme"

// Theme is one palette, as the page and its exports paint it.
type Theme = theme.Theme

// FontFamily and Roughness are the two appState defaults every palette
// shares.
const (
	FontFamily = theme.FontFamily
	Roughness  = theme.Roughness
)
