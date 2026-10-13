package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"
)

type auditRow struct {
	ID                string          `json:"id"`
	RequestID         string          `json:"request_id"`
	CreatedAt         time.Time       `json:"created_at"`
	UserID            string          `json:"user_id"`
	Username          string          `json:"username"`
	AttemptedUsername string          `json:"attempted_username"`
	IP                string          `json:"ip"`
	Action            string          `json:"action"`
	Subject           string          `json:"subject"`
	Method            string          `json:"method"`
	Result            string          `json:"result"`
	Status            int             `json:"status"`
	DurationMS        int64           `json:"duration_ms"`
	Details           json.RawMessage `json:"details"`
}
type auditPage struct {
	Audits []auditRow `json:"audits"`
	Next   string     `json:"next_before"`
	raw    []byte
}

func (r *runner) audits(index int, query url.Values) auditPage {
	var page auditPage
	res := r.as(r.admins[index%len(r.admins)], index, "GET", "/api/admin/audits?"+query.Encode(), nil, 200)
	decode(res.raw, &page)
	page.raw = res.raw
	return page
}
func (r *runner) audit() {
	password := testPassword()
	u := r.createUser("auditor", password, []grant{{r.namespace, "cases", "write"}})
	c := r.session(0, u.Username, password)
	k := r.key("audit-blackbox")
	contentSecret := r.namespace + "-content-secret"
	descriptionSecret := r.namespace + "-description-secret"
	tagSecret := r.namespace + "-tag-secret"
	wrongPassword := testPassword()
	expected := map[string]struct {
		action, method string
		status         int
	}{}
	audited := func(index int, method, path string, body any, action string, status int) response {
		res := r.send(c, index, method, path, body, http.Header{"X-Forwarded-For": {"203.0.113.123"}, "X-Real-Ip": {"203.0.113.123"}})
		must(res.err)
		require(res.status == status, "%s %s expected %d, got %d", method, path, status, res.status)
		id := res.headers.Get("X-Request-ID")
		require(id != "", "audited response did not expose request ID")
		_, duplicate := expected[id]
		require(!duplicate, "requests reused audit request ID")
		expected[id] = struct {
			action, method string
			status         int
		}{action, method, status}
		return res
	}
	var s state
	for i := 0; i < 6; i++ {
		body := editBody(s, contentSecret+"-"+strconv.Itoa(i), "")
		body["description"] = descriptionSecret
		var m mutation
		decode(audited(i%len(r.addresses), "PUT", configPath(k), body, "config.publish", 200).raw, &m)
		s = m.State
	}
	rulesBody := baseline(s)
	rulesBody["source_version"] = 1
	rulesBody["rules"] = []rule{{ID: "audit-rule", Name: "audit rule", Enabled: true, Conditions: []condition{{Tag: "env", Operator: "eq", Values: []string{tagSecret}}}}}
	var m mutation
	decode(audited(1, "PUT", configPath(k)+"/rules", rulesBody, "config.rules", 200).raw, &m)
	s = m.State
	decode(audited(0, "PUT", configPath(k), editBody(s, contentSecret+"-beta", "beta"), "config.beta.save", 200).raw, &m)
	s = m.State
	audited(1, "GET", configPath(k), nil, "config.read", 200)
	audited(0, "GET", configPath(k)+"/versions/1", nil, "config.version.read", 200)
	audited(1, "POST", configPath(k)+"/simulate", object{"tags": object{"env": tagSecret}}, "config.simulate", 200)
	stale := s
	stale.Revision--
	audited(1, "PUT", configPath(k), editBody(stale, contentSecret+"-rejected", ""), "config.publish", 409)
	audited(0, "POST", "/api/admin/login", object{"username": u.Username, "password": wrongPassword}, "auth.login", 401)
	audited(1, "POST", "/api/admin/namespaces", object{"name": r.namespace + "-denied"}, "namespace.create", 403)
	r.as(c, 0, "GET", "/api/admin/audits", nil, 403)
	query := url.Values{"username": {u.Username}, "limit": {"100"}}
	var rows []auditRow
	var auditJSON []byte
	r.wait("audit requests completed on another replica", func() bool {
		page := r.audits(1, query)
		rows, auditJSON = page.Audits, page.raw
		seen := map[string]bool{}
		for _, a := range rows {
			if a.Result != "pending" {
				seen[a.RequestID] = true
			}
		}
		for id := range expected {
			if !seen[id] {
				return false
			}
		}
		return true
	})
	conflictIP := ""
	requestIDs := map[string]bool{}
	for _, a := range rows {
		require(!requestIDs[a.RequestID], "request produced duplicate audit records")
		requestIDs[a.RequestID] = true
		if a.Action == "config.publish" && a.Status == 409 {
			conflictIP = a.IP
		}
		require(!a.CreatedAt.IsZero() && a.DurationMS >= 0, "audit timestamp/duration missing")
		ip, err := netip.ParseAddr(a.IP)
		require(err == nil && ip.Unmap().String() == a.IP && a.IP != "203.0.113.123", "audit trusted a forged IP or returned invalid address")
		if want, ok := expected[a.RequestID]; ok {
			require(a.Action == want.action && a.Method == want.method && a.Status == want.status, "audit operation/status differ from request")
			result := "success"
			if want.status >= 300 {
				result = "failure"
			}
			require(a.Result == result, "audit result differs from response")
			if a.Action == "auth.login" && a.Status == 401 {
				require(a.UserID == "" && a.Username == "" && a.AttemptedUsername == u.Username, "failed login impersonated a verified user")
			} else {
				require(a.UserID == u.ID && a.Username == u.Username, "audit actor snapshot missing")
			}
		}
	}
	// Inspect only this disposable identity's records, not other operators' data.
	address, err := url.Parse(r.addresses[0] + "/api/admin")
	must(err)
	secrets := []string{password, wrongPassword, contentSecret, descriptionSecret, tagSecret, `"password":`, `"password_hash":`, `"auth_version":`, `"content":`, `"description":`, `"expected_revision":`}
	for _, cookie := range c.Jar.Cookies(address) {
		secrets = append(secrets, cookie.Value)
	}
	for _, secret := range secrets {
		require(!bytes.Contains(auditJSON, []byte(secret)), "audit leaked sensitive data")
	}
	combined := url.Values{"username": {u.Username}, "action": {"config.publish"}, "result": {"failure"}, "ip": {conflictIP}, "limit": {"100"}}
	failures := r.audits(0, combined).Audits
	require(len(failures) == 1 && failures[0].Status == 409, "combined audit filters failed")
	sample := failures[0]
	window := url.Values{"username": {u.Username}, "from": {sample.CreatedAt.Format(time.RFC3339Nano)}, "to": {sample.CreatedAt.Add(time.Microsecond).Format(time.RFC3339Nano)}, "limit": {"100"}}
	bounded := r.audits(1, window).Audits
	found := false
	for _, a := range bounded {
		require(a.CreatedAt.Equal(sample.CreatedAt), "audit time range leaked outside microsecond window")
		found = found || a.ID == sample.ID
	}
	require(found, "audit start boundary excluded the record")
	window.Set("to", window.Get("from"))
	require(len(r.audits(0, window).Audits) == 0, "audit end boundary was inclusive")

	// A new record between pages must not shift the remaining ID cursor pages.
	baselineIDs := map[string]bool{}
	for _, a := range rows {
		baselineIDs[a.ID] = true
	}
	query.Set("limit", "3")
	page := r.audits(0, query)
	decode(audited(1, "PUT", configPath(k), editBody(s, contentSecret+"-later", ""), "config.publish", 200).raw, &m)
	s = m.State
	seen := map[string]bool{}
	previous := int64(0)
	for {
		for _, a := range page.Audits {
			number, err := strconv.ParseInt(a.ID, 10, 64)
			must(err)
			require(number > 0 && !seen[a.ID] && baselineIDs[a.ID] && (previous == 0 || number < previous), "audit pagination duplicated, reordered or shifted records")
			seen[a.ID] = true
			previous = number
		}
		if page.Next == "" {
			break
		}
		require(len(page.Audits) > 0 && page.Next == page.Audits[len(page.Audits)-1].ID, "invalid audit cursor")
		query.Set("before", page.Next)
		page = r.audits(1, query)
	}
	require(len(seen) == len(baselineIDs), "audit pagination skipped records")
	for _, path := range []string{"/api/admin/me", "/api/admin/namespaces", "/api/admin/namespaces/" + r.namespace + "/groups/cases/configs", configPath(k) + "/versions"} {
		require(r.as(c, 1, "GET", path, nil, 200).headers.Get("X-Request-ID") == "", "background query was audited")
	}
	clientURL := "/api/client/config?" + url.Values{"namespace": {r.namespace}, "group": {k.Group}, "name": {k.Name}}.Encode()
	require(r.as(&http.Client{Timeout: r.timeout}, 1, "GET", clientURL, nil, 200).headers.Get("X-Request-ID") == "", "anonymous SDK read was audited")
	for _, raw := range []string{"limit=101", "before=invalid", "ip=invalid", "result=unknown", "from=invalid"} {
		r.status(1, "GET", "/api/admin/audits?"+raw, nil, 400)
	}
	// Deletion of both objects must leave the identity/object snapshots queryable.
	audited(1, "DELETE", configPath(k), baseline(s), "config.delete", 200)
	r.call(0, "DELETE", "/api/admin/users/"+u.ID, userBaseline(u), nil)
	r.as(c, 1, "GET", "/api/admin/me", nil, 401)
	query.Del("before")
	query.Set("limit", "100")
	r.wait("deleted actor audit snapshots retained", func() bool {
		rows = r.audits(1, query).Audits
		seen := map[string]bool{}
		for _, a := range rows {
			if a.Result != "pending" {
				seen[a.RequestID] = true
			}
		}
		for id := range expected {
			if !seen[id] {
				return false
			}
		}
		return true
	})
	for _, a := range rows {
		if a.Action == "config.publish" {
			require(a.UserID == u.ID && a.Username == u.Username && a.Subject == k.Namespace+" / "+k.Group+" / "+k.Name, "deleted identity/object lost audit snapshots")
		}
	}
}
