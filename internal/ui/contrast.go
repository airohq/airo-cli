package ui

import (
	"image/color"
	"math"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

const textContrast = 4.5

func rgb(c color.Color) color.RGBA {
	r, g, b, _ := c.RGBA()
	return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), 255}
}

func blend(from, to color.RGBA, amount float64) color.RGBA {
	mix := func(a, b uint8) uint8 {
		return uint8(math.Round(float64(a)*(1-amount) + float64(b)*amount))
	}
	return color.RGBA{mix(from.R, to.R), mix(from.G, to.G), mix(from.B, to.B), 255}
}

// Relative luminance and contrast follow WCAG's sRGB definition.
func luminance(c color.RGBA) float64 {
	linear := func(v uint8) float64 {
		x := float64(v) / 255
		if x <= 0.04045 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(c.R) + 0.7152*linear(c.G) + 0.0722*linear(c.B)
}

func contrast(a, b color.RGBA) float64 {
	x, y := luminance(a), luminance(b)
	return (max(x, y) + 0.05) / (min(x, y) + 0.05)
}

func (p Palette) readable(preferred, bg color.RGBA) color.Color {
	if p.Profile == colorprofile.ANSI256 {
		// Entries 16–255 have defined RGB values. Choose the closest readable
		// entry directly: rounding a corrected RGB colour can lose contrast.
		best, distance := ansi.IndexedColor(16), math.Inf(1)
		for i := 16; i < 256; i++ {
			candidate := rgb(ansi.IndexedColor(i))
			if contrast(candidate, bg) < textContrast {
				continue
			}
			dr := float64(candidate.R) - float64(preferred.R)
			dg := float64(candidate.G) - float64(preferred.G)
			db := float64(candidate.B) - float64(preferred.B)
			if d := dr*dr + dg*dg + db*db; d < distance {
				best, distance = ansi.IndexedColor(i), d
			}
		}
		return best
	}
	if contrast(preferred, bg) >= textContrast {
		return preferred
	}
	// Move towards whichever endpoint offers more contrast, preserving as
	// much of the preferred tone as possible. At least one endpoint exceeds
	// 4.5:1 for every opaque background.
	end := color.RGBA{0, 0, 0, 255}
	white := color.RGBA{255, 255, 255, 255}
	if contrast(white, bg) > contrast(end, bg) {
		end = white
	}
	lo, hi := 0.0, 1.0
	for range 12 {
		mid := (lo + hi) / 2
		if contrast(blend(preferred, end, mid), bg) >= textContrast {
			hi = mid
		} else {
			lo = mid
		}
	}
	return blend(preferred, end, hi)
}
