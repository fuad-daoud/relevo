package transcript

// renderAgy is the table for `agy -p --output-format stream-json`: agy streams
// tool steps but not assistant text, so only result.response carries the text.
func renderAgy(obj map[string]any) []string {
	switch str(obj["event"]) {
	case "step_update":
		su := asMap(obj["step_update"])
		if str(su["step_type"]) != "tool" {
			return nil
		}
		info := asMap(su["tool_info"])
		switch str(su["state"]) {
		case "ACTIVE":
			name := str(su["tool_name"])
			if name == "" {
				name = str(info["name"])
			}
			return []string{toolLine(name, asMap(info["parameters"]))}
		case "DONE":
			return []string{okLine(str(info["output"]))}
		case "ERROR":
			return []string{errLine(str(asMap(info["error"])["message"]))}
		}
		return []string{unknown(obj)}
	case "result":
		r := asMap(obj["result"])
		var out []string
		for _, d := range asList(r["denied_actions"]) {
			m := asMap(d)
			action := str(m["action"])
			if action == "" {
				continue
			}
			line := "denied: " + action
			if dn := str(m["display_name"]); dn != "" {
				line += " (" + dn + ")"
			}
			out = append(out, line)
		}
		if st := str(r["status"]); st != "" && st != "SUCCESS" {
			out = append(out, "result: "+st)
		}
		if msg := str(r["error"]); msg != "" {
			out = append(out, "error: "+oneLine(msg))
		}
		if resp := str(r["response"]); resp != "" {
			out = append(out, resp)
		}
		return out
	case "init":
		return nil
	}
	return []string{unknown(obj)}
}
