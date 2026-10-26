package dk

// MergeData combines snapshots from main and subcategory endpoints into one
// board. The first occurrence of each event/market/selection id wins, so a
// caller should pass the main snapshot before alternates or props.
func MergeData(datas ...*Data) *Data {
	out := &Data{
		Events:               []Event{},
		Markets:              []Market{},
		Selections:           []Selection{},
		SubscriptionPartials: map[string]SubscriptionPartial{},
	}
	events := map[string]bool{}
	markets := map[string]bool{}
	selections := map[string]bool{}
	for _, d := range datas {
		if d == nil {
			continue
		}
		for _, e := range d.Events {
			id := e.ID.String()
			if id == "" || !events[id] {
				events[id] = true
				out.Events = append(out.Events, e)
			}
		}
		for _, m := range d.Markets {
			id := m.ID.String()
			if id == "" || !markets[id] {
				markets[id] = true
				out.Markets = append(out.Markets, m)
			}
		}
		for _, s := range d.Selections {
			id := s.ID.String()
			if id == "" || !selections[id] {
				selections[id] = true
				out.Selections = append(out.Selections, s)
			}
		}
	}
	return out
}
