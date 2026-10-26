package dk

// NFLSubscribeMessage reproduces the subscription in probe-ws.cjs: the NFL
// league filter used for the first DraftKings observation.
func NFLSubscribeMessage() map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"method":  "subscribe",
		"id":      "nfl-events",
		"params": map[string]any{
			"entity": "events",
			"queryParams": map[string]any{
				"query":          "$filter=leagueId eq '88808'",
				"initialData":    false,
				"includeMarkets": "$filter=tags/all(t: t ne 'SportcastBetBuilder')",
				"projection":     "sportsbook",
				"locale":         "en-US",
			},
			"forwardedHeaders": map[string]any{},
			"clientMetadata": map[string]any{
				"feature":          "odds-research",
				"X-Client-Name":    "web",
				"X-Client-Version": "unknown",
			},
			"jwt":      "",
			"siteName": "dkcaon",
		},
	}
}

// PartialsSubscribeMessages builds one subscribe frame per entry returned in
// snapshot.subscriptionPartials, like compare-dk.cjs.
//
// Every key of the partial (query, includeMarkets, locale, and any future
// key) is forwarded. initialData is forced to false because the SDK setting
// is the one that works, and projection is set to "sportsbook".
func PartialsSubscribeMessages(partials []SubscriptionPartial, feature, version string) []map[string]any {
	if feature == "" {
		feature = "leagueSubcategory"
	}
	if version == "" {
		version = "2643.4.1.10"
	}
	msgs := make([]map[string]any, 0, len(partials))
	for i, partial := range partials {
		queryParams := make(map[string]any, len(partial)+2)
		for k, v := range partial {
			if k == "entity" {
				continue
			}
			queryParams[k] = v
		}
		queryParams["initialData"] = false
		queryParams["projection"] = "sportsbook"

		msgs = append(msgs, map[string]any{
			"jsonrpc": "2.0",
			"method":  "subscribe",
			"id":      subscriptionID(i),
			"params": map[string]any{
				"entity":           partial.Entity(),
				"queryParams":      queryParams,
				"forwardedHeaders": map[string]any{},
				"clientMetadata": map[string]any{
					"feature":          feature,
					"X-Client-Name":    "web",
					"X-Client-Version": version,
				},
				"jwt":      "",
				"siteName": "dkcaon",
			},
		})
	}
	return msgs
}

func subscriptionID(i int) string {
	return "race-" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
