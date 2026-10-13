package main

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"reflect"

	sdk "github.com/vincent-vinf/confhub/sdk/go"
)

type grant struct {
	Namespace string `json:"namespace"`
	Group     string `json:"group"`
	Access    string `json:"access"`
}
type testUser struct {
	ID          string  `json:"id"`
	Username    string  `json:"username"`
	Revision    int64   `json:"revision"`
	Enabled     bool    `json:"enabled"`
	ClientsRead bool    `json:"clients_read"`
	Grants      []grant `json:"grants"`
}

// Keep replaying the original JWT, even when a response clears its cookie.
// This checks server-side revocation rather than only browser cookie removal.
type fixedSessionJar struct{ http.CookieJar }

func (fixedSessionJar) SetCookies(*url.URL, []*http.Cookie) {}

func testPassword() string {
	raw := make([]byte, 16)
	_, err := rand.Read(raw)
	must(err)
	return hex.EncodeToString(raw)
}
func (r *runner) as(client *http.Client, index int, method, path string, body any, want int) response {
	res := r.send(client, index, method, path, body, nil)
	must(res.err)
	require(res.status == want, "%s %s expected %d, got %d", method, path, want, res.status)
	return res
}
func (r *runner) session(index int, name, password string) *http.Client {
	jar, err := cookiejar.New(nil)
	must(err)
	c := &http.Client{Jar: jar, Timeout: r.timeout}
	r.as(c, index, "GET", "/api/admin/me", nil, 401)
	r.as(c, index, "POST", "/api/admin/login", object{"username": name, "password": password}, 200)
	loginURL, err := url.Parse(r.addresses[index] + "/api/admin")
	must(err)
	cookies := jar.Cookies(loginURL)
	require(len(cookies) > 0, "login did not issue a session cookie")
	for _, cookie := range cookies {
		cookie.Path = "/api/admin"
	}
	// Replica addresses may use different hosts; replay the same session on each.
	for _, address := range r.addresses {
		endpoint, err := url.Parse(address + "/api/admin")
		must(err)
		jar.SetCookies(endpoint, cookies)
	}
	c.Jar = fixedSessionJar{jar}
	return c
}
func (r *runner) createUser(suffix, password string, grants []grant) testUser {
	name := r.namespace + "-" + suffix
	r.users = append(r.users, name)
	var u testUser
	r.call(0, "POST", "/api/admin/users", object{"username": name, "password": password, "enabled": true, "grants": grants}, &u)
	require(u.ID != "" && u.Username == name, "user creation identity mismatch")
	return u
}
func userBaseline(u testUser) object {
	return object{"expected_revision": u.Revision, "confirmed": true}
}
func userEdit(u testUser, enabled, clients bool, grants []grant) object {
	b := userBaseline(u)
	b["enabled"], b["clients_read"], b["grants"] = enabled, clients, grants
	return b
}
func (r *runner) updateUser(index int, u testUser, enabled, clients bool, grants []grant) testUser {
	var next testUser
	r.call(index, "PUT", "/api/admin/users/"+u.ID, userEdit(u, enabled, clients, grants), &next)
	return next
}
func (r *runner) findUser(name string) (testUser, bool) {
	after := ""
	for {
		var users []testUser
		r.call(0, "GET", "/api/admin/users?limit=100&after="+url.QueryEscape(after), nil, &users)
		for _, u := range users {
			if u.Username == name {
				return u, true
			}
		}
		if len(users) < 100 {
			return testUser{}, false
		}
		next := users[len(users)-1].Username
		require(next > after, "user pagination stalled")
		after = next
	}
}
func (r *runner) targetKey(k sdk.Key, group string) sdk.Key {
	k.Group = group
	r.mu.Lock()
	r.keys = append(r.keys, k)
	r.mu.Unlock()
	return k
}

func (r *runner) permissions() {
	k := r.key("permission-source")
	s := r.save(0, k, state{}, "one", "").State
	s = r.save(0, k, s, "two", "").State
	s = r.rules(k, s, grayRules(), 1)
	s = r.save(0, k, s, "permission-beta", "beta").State
	hidden := r.targetKey(r.key("permission-hidden"), "other")
	r.save(1, hidden, state{}, "hidden", "")
	h := r.read(hidden)
	password := testPassword()
	readOnly := []grant{{r.namespace, "cases", "read"}}
	u := r.createUser("reader", password, readOnly)
	c := r.session(0, u.Username, password)
	for i := range r.addresses {
		res := r.as(c, i, "GET", "/api/admin/me", nil, 200)
		var me testUser
		decode(res.raw, &me)
		require(me.ID == u.ID && reflect.DeepEqual(me.Grants, readOnly), "identity or permissions differ across replicas")
		var visible state
		decode(r.as(c, i, "GET", configPath(k), nil, 200).raw, &visible)
		require(reflect.DeepEqual(visible, s), "read-only user cannot see complete main/beta state")
		r.as(c, i, "GET", configPath(hidden), nil, 403)
	}
	var names, groups []string
	decode(r.as(c, 1, "GET", "/api/admin/namespaces", nil, 200).raw, &names)
	decode(r.as(c, 1, "GET", "/api/admin/namespaces/"+r.namespace+"/groups", nil, 200).raw, &groups)
	require(reflect.DeepEqual(names, []string{r.namespace}) && reflect.DeepEqual(groups, []string{"cases"}), "organization lists leaked unreadable scopes")
	for _, suffix := range []string{"/versions", "/versions/1"} {
		r.as(c, 1, "GET", configPath(k)+suffix, nil, 200)
	}
	r.as(c, 1, "POST", configPath(k)+"/simulate", object{"tags": object{}}, 200)
	for _, op := range []operation{
		{"PUT", configPath(k), editBody(s, "denied", "")}, {"DELETE", configPath(k), baseline(s)},
		{"PUT", configPath(k) + "/rules", baseline(s)}, {"POST", configPath(k) + "/rollback", baseline(s)}, {"POST", configPath(k) + "/promote", baseline(s)},
		{"POST", "/api/admin/users", object{}}, {"PUT", "/api/admin/users/" + u.ID, userEdit(u, true, true, []grant{{"", "", "write"}})},
		{"POST", "/api/admin/namespaces", object{"name": r.namespace + "-denied"}},
	} {
		r.as(c, 1, op.method, op.path, op.body, 403)
	}
	for _, path := range []string{"/api/admin/users", "/api/admin/audits", "/api/admin/clients", "/api/admin/client-tags"} {
		r.as(c, 1, "GET", path, nil, 403)
	}
	require(reflect.DeepEqual(r.read(k), s), "read-only requests changed configuration")

	writeCases := []grant{{r.namespace, "cases", "write"}}
	u = r.updateUser(1, u, true, true, writeCases)
	var m mutation
	decode(r.as(c, 0, "PUT", configPath(k), editBody(s, "written", ""), 200).raw, &m)
	require(m.Changed && m.State.Revision == s.Revision+1 && m.State.Versions[3].Content == "written", "upgraded user did not publish the expected main version")
	s = r.read(k)
	r.as(c, 0, "GET", "/api/admin/clients", nil, 200)
	r.as(c, 1, "GET", "/api/admin/client-tags", nil, 200)
	r.as(c, 0, "PUT", configPath(hidden), editBody(h, "denied", ""), 403)
	r.as(c, 1, "POST", "/api/admin/configs/batch-delete", batchBody([]sdk.Key{k, hidden}, []state{s, h}, ""), 403)
	r.as(c, 1, "POST", "/api/admin/configs/batch-move", batchBody([]sdk.Key{k}, []state{s}, "other"), 403)
	require(reflect.DeepEqual(r.read(k), s) && reflect.DeepEqual(r.read(hidden), h), "unauthorized batch partially mutated configurations")

	writeNamespace := []grant{{r.namespace, "", "write"}}
	u = r.updateUser(0, u, true, true, writeNamespace)
	destination := r.targetKey(k, "other")
	r.as(c, 1, "POST", "/api/admin/configs/batch-move", batchBody([]sdk.Key{k}, []state{s}, "other"), 200)
	r.status(0, "GET", configPath(k), nil, 404)
	s = r.read(destination)
	require(s.Beta != nil && s.Beta.Content == "permission-beta" && s.Global == 3, "authorized move lost history or beta")
	// Downgrading the existing session takes effect on every actual endpoint.
	u = r.updateUser(1, u, true, false, readOnly)
	for i := range r.addresses {
		r.as(c, i, "GET", configPath(hidden), nil, 403)
		r.as(c, i, "PUT", configPath(destination), editBody(s, "denied-after-downgrade", ""), 403)
		r.as(c, i, "GET", "/api/admin/clients", nil, 403)
	}
	u = r.updateUser(0, u, true, false, []grant{})
	decode(r.as(c, 1, "GET", "/api/admin/namespaces", nil, 200).raw, &names)
	require(len(names) == 0, "user without grants can see namespaces")
	results := r.race([]operation{
		{"PUT", "/api/admin/users/" + u.ID, userEdit(u, true, false, readOnly)},
		{"PUT", "/api/admin/users/" + u.ID, userEdit(u, true, true, writeNamespace)},
	})
	index := winner(results)
	decode(results[index].raw, &u)
	for i := range r.addresses {
		var me testUser
		decode(r.as(c, i, "GET", "/api/admin/me", nil, 200).raw, &me)
		require(reflect.DeepEqual(me.Grants, u.Grants) && me.ClientsRead == u.ClientsRead, "concurrent grant update not applied across replicas")
	}
	u = r.updateUser(0, u, false, u.ClientsRead, u.Grants)
	for i := range r.addresses {
		r.as(c, i, "GET", "/api/admin/me", nil, 401)
	}
	r.as(c, 1, "POST", "/api/admin/login", object{"username": u.Username, "password": password}, 401)
	u = r.updateUser(1, u, true, u.ClientsRead, u.Grants)
	for i := range r.addresses {
		r.as(c, i, "GET", "/api/admin/me", nil, 401)
	}
	c = r.session(1, u.Username, password)
	replacement := testPassword()
	b := userBaseline(u)
	b["password"] = replacement
	r.call(0, "POST", "/api/admin/users/"+u.ID+"/password", b, nil)
	for i := range r.addresses {
		r.as(c, i, "GET", "/api/admin/me", nil, 401)
	}
	r.as(c, 0, "POST", "/api/admin/login", object{"username": u.Username, "password": password}, 401)
	c = r.session(1, u.Username, replacement)
	finalPassword := testPassword()
	r.as(c, 0, "POST", "/api/admin/password", object{"old_password": replacement, "new_password": finalPassword}, 200)
	for i := range r.addresses {
		r.as(c, i, "GET", "/api/admin/me", nil, 401)
	}
	c = r.session(1, u.Username, finalPassword)
	var found bool
	u, found = r.findUser(u.Username)
	require(found, "test user vanished before cleanup")
	r.call(0, "DELETE", "/api/admin/users/"+u.ID, userBaseline(u), nil)
	for i := range r.addresses {
		r.as(c, i, "GET", "/api/admin/me", nil, 401)
	}
}
