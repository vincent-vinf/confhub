package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"

	sdk "github.com/vincent-vinf/confhub/sdk/go"
)

func archiveZIP(group string, bodies map[string]string) []byte {
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	names := make([]string, 0, len(bodies))
	for name := range bodies {
		names = append(names, name)
	}
	sort.Strings(names)
	metadata := "metadata:\n"
	for _, name := range names {
		f, e := w.Create(group + "/" + name)
		must(e)
		_, e = io.WriteString(f, bodies[name])
		must(e)
		metadata += fmt.Sprintf("- group: %s\n  dataId: %s\n  type: text\n  desc: imported\n  appName: ''\n", group, name)
	}
	f, e := w.Create(".metadata.yml")
	must(e)
	_, e = io.WriteString(f, metadata)
	must(e)
	must(w.Close())
	return out.Bytes()
}
func (r *runner) archiveUpload(index int, data []byte, policy string, plan object, want int) object {
	var out bytes.Buffer
	w := multipart.NewWriter(&out)
	f, e := w.CreateFormFile("file", "synthetic.zip")
	must(e)
	_, e = f.Write(data)
	must(e)
	must(w.WriteField("namespace", r.namespace))
	must(w.WriteField("policy", policy))
	path := "/api/admin/configs/import/preview"
	if plan != nil {
		raw, e := json.Marshal(plan)
		must(e)
		must(w.WriteField("plan", string(raw)))
		must(w.WriteField("confirmed", "true"))
		path = "/api/admin/configs/import"
	}
	must(w.Close())
	ctx, cancel := context.WithTimeout(r.ctx, r.timeout)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "POST", r.addresses[index]+path, &out)
	must(e)
	req.Header.Set("Origin", r.addresses[index])
	req.Header.Set("Content-Type", w.FormDataContentType())
	res, e := r.admins[index].Do(req)
	must(e)
	defer res.Body.Close()
	raw, e := io.ReadAll(res.Body)
	must(e)
	require(res.StatusCode == want, "archive upload status %d want %d", res.StatusCode, want)
	var result object
	decode(raw, &result)
	return result
}
func (r *runner) archive() {
	keys := []sdk.Key{r.key("archive-a"), r.key("archive-b")}
	states := make([]state, len(keys))
	for i, k := range keys {
		states[i] = r.save(0, k, state{}, "initial", "").State
		states[i] = r.rules(k, states[i], grayRules(), 1)
		states[i] = r.save(0, k, states[i], "beta-kept", "beta").State
	}
	normal := r.sdk([]string{r.addresses[1]}, map[string]string{"env": "normal"}, "")
	defer closeSDK(normal)
	gray := r.sdk([]string{r.addresses[len(r.addresses)-1]}, map[string]string{"env": "gray", "sys.ip": "192.168.2.3"}, "")
	defer closeSDK(gray)
	no, goGray := newObserver(), newObserver()
	pn := r.pythonProbe(map[string]string{"env": "normal"}, "archive-normal")
	defer pn.close()
	pg := r.pythonProbe(map[string]string{"env": "gray", "sys.ip": "192.168.2.3"}, "archive-gray")
	defer pg.close()
	for i, k := range keys {
		must(normal.Subscribe(r.ctx, k, no.callback))
		must(gray.Subscribe(r.ctx, k, goGray.callback))
		pn.rpc(object{"op": "subscribe", "key": k})
		pg.rpc(object{"op": "subscribe", "key": k})
		r.observed(no, k, states[i], "initial", "", 1)
		r.observed(goGray, k, states[i], "beta-kept", "specific", 0)
	}
	data := archiveZIP(keys[0].Group, map[string]string{keys[0].Name: "imported-a\r\n", keys[1].Name: "imported-b\n"})
	plan := r.archiveUpload(0, data, "OVERWRITE", nil, 200)
	states[1] = r.save(1, keys[1], states[1], "concurrent", "").State
	r.archiveUpload(1, data, "OVERWRITE", plan, 409)
	require(r.read(keys[0]).Revision == states[0].Revision, "failed import partially changed first config")
	plan = r.archiveUpload(0, data, "OVERWRITE", nil, 200)
	result := r.archiveUpload(1, data, "OVERWRITE", plan, 200)
	require(result["updated"] == float64(2), "import did not update both configs")
	for i, k := range keys {
		s := r.read(k)
		content := []string{"imported-a\r\n", "imported-b\n"}[i]
		require(s.Global == states[i].Global+1 && s.Beta.Content == "beta-kept" && len(s.Rules) == len(states[i].Rules), "import lost versions/beta/rules")
		r.observed(no, k, s, content, "", s.Global)
		r.observed(goGray, k, s, "beta-kept", "specific", 0)
		r.get(normal, k, s, content, "", s.Global)
		r.get(gray, k, s, "beta-kept", "specific", 0)
		r.pythonObserved(pn, k, s, content, "", s.Global)
		r.pythonObserved(pg, k, s, "beta-kept", "specific", 0)
	}
	plan = r.archiveUpload(0, data, "OVERWRITE", nil, 200)
	result = r.archiveUpload(1, data, "OVERWRITE", plan, 200)
	require(result["unchanged"] == float64(2), "duplicate import published versions")
	plan = r.archiveUpload(0, data, "SKIP", nil, 200)
	result = r.archiveUpload(1, data, "SKIP", plan, 200)
	require(result["skipped"] == float64(2), "skip policy failed")
	plan = r.archiveUpload(0, data, "ABORT", nil, 200)
	require(plan["can_import"] == false, "abort preview did not report conflicts")
	r.archiveUpload(1, data, "ABORT", plan, 409)
	query := url.Values{"namespace": {r.namespace}, "group": {keys[0].Group}, "search": {"archive-"}}
	rawStatus, raw, e := r.request(0, "GET", "/api/admin/configs/export?"+query.Encode(), nil)
	must(e)
	require(rawStatus == 200, "export failed")
	z, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	must(e)
	require(len(z.File) == 3, "export search included unrelated configs")
	for _, f := range z.File {
		if f.Name == ".metadata.yml" {
			continue
		}
		reader, e := f.Open()
		must(e)
		body, e := io.ReadAll(reader)
		reader.Close()
		must(e)
		expected := map[string]string{keys[0].Group + "/" + keys[0].Name: "imported-a\r\n", keys[1].Group + "/" + keys[1].Name: "imported-b\n"}
		require(string(body) == expected[f.Name], "export changed content")
	}
}
