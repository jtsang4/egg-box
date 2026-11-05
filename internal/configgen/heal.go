package configgen

import (
	"regexp"
	"strconv"

	"eggbox/internal/subscription"
)

// PathIssue is a problem the agent reported at a JSON path of the configuration.
type PathIssue struct {
	Path    string
	Message string
}

var rePathIndex = regexp.MustCompile(`^\$\.(outbounds|endpoints)\[(\d+)\]`)

// Heal removes the nodes the issues point at, so one provider-supplied node
// the agent rejects (an option its policy does not allow, a type the build
// lacks) does not stop all the others from working. It returns the reduced
// input, the nodes it dropped and whether anything changed. Issues that do not
// point at a node are not healable and leave the input as it was.
func Heal(in Input, out *Output, issues []PathIssue) (Input, []Dropped, bool) {
	drop := map[string]string{} // key -> reason
	for _, is := range issues {
		m := rePathIndex.FindStringSubmatch(is.Path)
		if m == nil {
			continue
		}
		idx, _ := strconv.Atoi(m[2])
		keys := out.Layout.Outbounds
		if m[1] == "endpoints" {
			keys = out.Layout.Endpoints
		}
		if idx < 0 || idx >= len(keys) || keys[idx] == "" {
			continue
		}
		if _, seen := drop[keys[idx]]; !seen {
			drop[keys[idx]] = is.Message
		}
	}
	if len(drop) == 0 {
		return in, nil, false
	}
	var kept []subscription.Node
	var dropped []Dropped
	for _, n := range in.Nodes {
		if reason, ok := drop[n.Key()]; ok {
			dropped = append(dropped, Dropped{Name: n.Name, Key: n.Key(), Reason: reason})
			continue
		}
		kept = append(kept, n)
	}
	in.Nodes = kept
	return in, dropped, len(dropped) > 0
}
