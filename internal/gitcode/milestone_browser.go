package gitcode

import (
	"net/url"
	"strconv"
	"strings"
)

// normalizeMilestoneBrowserIdentity handles the captured v5 locator shape:
// number is the provider ID, while url's milestone suffix is the local iid.
// A canonical provider-ID suffix alone does not establish a local iid.
func normalizeMilestoneBrowserIdentity(remoteID string, explicitIID any, rawURL string) (string, string, error) {
	iid := ""
	bad := func() (string, string, error) {
		return "", "", &ErrSchemaDecode{Field: "milestone.iid", Expected: "consistent positive repository-local integer", Received: "invalid or conflicting identity"}
	}
	accept := func(value string) bool {
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return false
		}
		canonical := strconv.Itoa(n)
		if iid != "" && iid != canonical {
			return false
		}
		iid = canonical
		return true
	}
	if !milestoneIdentityMissing(explicitIID) {
		value, err := decodeMilestoneID(explicitIID)
		if err != nil || !accept(value) {
			return bad()
		}
	}
	if rawURL == "" {
		return iid, "", nil
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.Opaque != "" {
		return iid, "", nil
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return bad()
	}
	if values, ok := query["iid"]; ok {
		if len(values) != 1 || !accept(values[0]) {
			return bad()
		}
	}
	parts := strings.Split(strings.TrimRight(u.Path, "/"), "/")
	if len(parts) < 3 || parts[len(parts)-2] != "milestones" {
		return iid, "", nil
	}
	suffix := parts[len(parts)-1]
	n, err := strconv.Atoi(suffix)
	if err != nil || n <= 0 {
		return iid, "", nil
	}
	if strconv.Itoa(n) != remoteID && !accept(suffix) {
		return bad()
	}
	parts[len(parts)-1] = remoteID
	u.Path = strings.Join(parts, "/")
	u.RawPath = ""
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	u.RawFragment = ""
	u.ForceQuery = false
	if iid != "" {
		u.RawQuery = url.Values{"iid": []string{iid}}.Encode()
	}
	return iid, u.String(), nil
}

func (c *HTTPClient) milestoneBrowserURL(owner, repo string, m Milestone) string {
	if m.HTMLURL != "" {
		return m.HTMLURL
	}
	u, err := url.Parse(c.browserBaseURL())
	if err != nil {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	u.ForceQuery = false
	u.RawPath = ""
	u.Path = strings.TrimRight(u.Path, "/") + "/" + owner + "/" + repo + "/milestones/" + m.RemoteID
	if m.IID != "" {
		u.RawQuery = url.Values{"iid": []string{m.IID}}.Encode()
	}
	return u.String()
}

// SanitizeMilestoneBrowserURL preserves only a verified positive iid query.
func SanitizeMilestoneBrowserURL(remoteID, rawURL string) string {
	_, locator, err := normalizeMilestoneBrowserIdentity(remoteID, nil, rawURL)
	if err != nil {
		return ""
	}
	return locator
}
