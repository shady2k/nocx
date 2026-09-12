package framebytes

import "strconv"

func strconv2(n int) string { return strconv.Itoa(n) }
func strconvFloat(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
