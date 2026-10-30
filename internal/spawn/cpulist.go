package spawn

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// cpuListPattern is a systemd cpu-list: comma-separated numbers or ranges.
var cpuListPattern = regexp.MustCompile(`^[0-9]+(-[0-9]+)?(,[0-9]+(-[0-9]+)?)*$`)

// ParseCPUList parses a systemd cpu-list ("0-2", "1,3,5-7") into sorted,
// de-duplicated cores; no core above 1023 is accepted. Pure.
func ParseCPUList(s string) ([]int, error) {
	if !cpuListPattern.MatchString(s) {
		return nil, fmt.Errorf("not a cpu list")
	}
	seen := make(map[int]bool)
	var out []int
	for _, part := range strings.Split(s, ",") {
		lo, hi := 0, 0
		if dash := strings.Index(part, "-"); dash >= 0 {
			var err error
			lo, err = strconv.Atoi(part[:dash])
			if err != nil {
				return nil, fmt.Errorf("not a cpu list")
			}
			hi, err = strconv.Atoi(part[dash+1:])
			if err != nil {
				return nil, fmt.Errorf("not a cpu list")
			}
			if lo > hi {
				return nil, fmt.Errorf("range %d-%d runs backwards", lo, hi)
			}
		} else {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("not a cpu list")
			}
			lo, hi = n, n
		}
		for n := lo; n <= hi; n++ {
			if n > 1023 {
				return nil, fmt.Errorf("cpu %d is above 1023", n)
			}
		}
		for n := lo; n <= hi; n++ {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Ints(out)
	return out, nil
}
