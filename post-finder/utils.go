package post_finder

import (
	"sort"
)

func AppendUint64SliceOrderly(a []uint64, x uint64) []uint64 {
	i := func(a []uint64, x uint64) int {
		return sort.Search(len(a), func(i int) bool { return a[i] >= x })
	}(a, x)

	return append(append(a[:i], x), a[i:]...)
}
