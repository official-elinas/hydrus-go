//go:build fyne

package fyneapp

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

const (
	hydrusTagColorCreator            fyne.ThemeColorName = "hydrusTagCreator"
	hydrusTagColorSeries             fyne.ThemeColorName = "hydrusTagSeries"
	hydrusTagColorCharacter          fyne.ThemeColorName = "hydrusTagCharacter"
	hydrusTagColorUnnamespaced       fyne.ThemeColorName = "hydrusTagUnnamespaced"
	hydrusTagColorNamespacedFallback fyne.ThemeColorName = "hydrusTagNamespacedFallback"
)

type forcedDarkTheme struct{}

func (forcedDarkTheme) Color(name fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	switch name {
	case hydrusTagColorCreator:
		return color.NRGBA{R: 170, G: 0, B: 0, A: 255}
	case hydrusTagColorSeries:
		return color.NRGBA{R: 170, G: 0, B: 170, A: 255}
	case hydrusTagColorCharacter:
		return color.NRGBA{R: 0, G: 170, B: 0, A: 255}
	case hydrusTagColorUnnamespaced:
		return color.NRGBA{R: 0, G: 111, B: 250, A: 255}
	case hydrusTagColorNamespacedFallback:
		return color.NRGBA{R: 114, G: 160, B: 193, A: 255}
	}

	return theme.DefaultTheme().Color(name, theme.VariantDark)
}

func (forcedDarkTheme) Font(style fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(style)
}

func (forcedDarkTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

func (forcedDarkTheme) Size(name fyne.ThemeSizeName) float32 {
	return theme.DefaultTheme().Size(name)
}
