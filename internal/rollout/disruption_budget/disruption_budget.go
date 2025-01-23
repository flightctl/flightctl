package disruption_budget

import (
	"errors"
	"fmt"
	"strings"

	"github.com/samber/lo"
)

const keySeparator = "@#$"

type Counts struct {
	TotalCount int
	BusyCount  int
	key        map[string]any
}

type GroupMap struct {
	counts  map[string]*Counts
	groupBy []string
}

func NewGroupMap(groupBy []string) *GroupMap {
	return &GroupMap{
		counts:  make(map[string]*Counts),
		groupBy: groupBy,
	}
}

func (g *GroupMap) key(row map[string]any) (string, error) {
	var (
		formatParts []string
		values      []any
	)
	for _, gb := range g.groupBy {
		v, ok := row[gb]
		if !ok {
			return "", fmt.Errorf("group by field %s was not found", gb)
		}
		formatParts = append(formatParts, "%v")
		values = append(values, v)
	}
	format := strings.Join(formatParts, keySeparator)
	return fmt.Sprintf(format, values...), nil
}

func (g *GroupMap) count(row map[string]any) (int, error) {
	countAsAny, ok := row["count"]
	if !ok {
		return 0, errors.New("count field was not found")
	}
	count, ok := countAsAny.(int64)
	if !ok {
		return 0, errors.New("count is not an integer type")
	}
	return int(count), nil
}

func (g *GroupMap) getOrCreate(row map[string]any) (*Counts, error) {
	key, err := g.key(row)
	if err != nil {
		return nil, err
	}
	counts, exists := g.counts[key]
	if !exists {
		counts = &Counts{
			key: lo.SliceToMap(g.groupBy, func(k string) (string, any) { return k, row[k] }),
		}
		g.counts[key] = counts
	}
	return counts, nil
}

func (g *GroupMap) insert(row map[string]any, setter func(*Counts, int)) error {
	counts, err := g.getOrCreate(row)
	if err != nil {
		return err
	}
	count, err := g.count(row)
	if err != nil {
		return err
	}
	setter(counts, count)
	return nil
}

func (g *GroupMap) InsertTotal(row map[string]any) error {
	return g.insert(row, func(counts *Counts, count int) { counts.TotalCount = count })
}

func (g *GroupMap) InsertBusy(row map[string]any) error {
	return g.insert(row, func(counts *Counts, count int) { counts.BusyCount = count })
}

func mergeDeviceAllowanceCounts(totalMaps, busyMaps []map[string]any, groupBy []string) (map[string]*Counts, error) {
	groupMap := NewGroupMap(groupBy)
	for _, m := range totalMaps {
		if err := groupMap.InsertTotal(m); err != nil {
			return nil, err
		}
	}
	for _, m := range busyMaps {
		if err := groupMap.InsertBusy(m); err != nil {
			return nil, err
		}
	}
	return groupMap.counts, nil
}
