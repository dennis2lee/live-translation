package translator

import "strconv"

func formatMS(v float64) string      { return strconv.FormatFloat(v, 'f', 0, 64) }
func formatMinutes(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
func itoa(v int) string              { return strconv.Itoa(v) }
