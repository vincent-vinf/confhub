package configarchive_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"github.com/vincent-vinf/confhub/internal/config"
	"reflect"
	"strings"
	"testing"

	"github.com/vincent-vinf/confhub/internal/configarchive"
)

func nacosZIP(t *testing.T, entries [][2]string) []byte {
	t.Helper()
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for _, entry := range entries {
		f, err := w.Create(entry[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(entry[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestNacosNewArchivePreservesNamesTextAndMetadata(t *testing.T) {
	data := nacosZIP(t, [][2]string{{"组/app.yaml", "# 中文\r\nvalue: 1\r\n"}, {".metadata.yml", "metadata:\n- group: 组\n  dataId: app.yaml\n  type: yaml\n  desc: 说明\n  appName: ignored\n"}})
	p, err := configarchive.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || p.Items[0].Content != "# 中文\r\nvalue: 1\r\n" || p.Items[0].Description != "说明" || p.Items[0].Name != "app.yaml" {
		t.Fatalf("lost original: %+v", p)
	}
	exported, err := configarchive.Encode(p.Items)
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(exported), int64(len(exported)))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.File) != 2 {
		t.Fatalf("entries: %d", len(r.File))
	}
	again, err := configarchive.Parse(exported)
	if err != nil || again.Items[0] != p.Items[0] {
		t.Fatalf("interchange: %+v %v", again, err)
	}
}

func TestRejectsUnsafeOrInconsistentPackages(t *testing.T) {
	meta := "metadata:\n- group: G\n  dataId: app\n  type: text\n"
	cases := map[string][][2]string{
		"old format":         {{"G/app", "ok"}, {".meta.yml", meta}},
		"traversal":          {{"../app", "ok"}, {".metadata.yml", meta}},
		"absolute":           {{"/G/app", "ok"}, {".metadata.yml", meta}},
		"backslash":          {{`G\app`, "ok"}, {".metadata.yml", meta}},
		"extra body":         {{"G/app", "ok"}, {"G/extra", "x"}, {".metadata.yml", meta}},
		"missing body":       {{".metadata.yml", meta}},
		"duplicate file":     {{"G/app", "a"}, {"G/app", "b"}, {".metadata.yml", meta}},
		"duplicate metadata": {{"G/app", "a"}, {".metadata.yml", meta + "- group: G\n  dataId: app\n  type: text\n"}},
		"duplicate yaml key": {{"G/app", "a"}, {".metadata.yml", meta + "  type: yaml\n"}},
		"unknown type":       {{"G/app", "a"}, {".metadata.yml", strings.ReplaceAll(meta, "text", "unknown")}},
		"invalid json":       {{"G/app", "{"}, {".metadata.yml", strings.ReplaceAll(meta, "text", "json")}},
		"invalid UTF8":       {{"G/app", string([]byte{255})}, {".metadata.yml", meta}},
		"oversized body":     {{"G/app", strings.Repeat("a", (1<<20)+1)}, {".metadata.yml", meta}},
		"extra document":     {{"G/app", "ok"}, {".metadata.yml", meta + "---\nmetadata: []\n"}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := configarchive.Parse(nacosZIP(t, entries)); !errors.Is(err, config.ErrInvalid) {
				t.Fatalf("unsafe package accepted: %v", err)
			}
		})
	}
}

func TestAllSupportedFormatsAndLimits(t *testing.T) {
	items := []configarchive.Item{{Group: "G", Name: "plain", Format: "text", Content: ""}, {Group: "G", Name: "json", Format: "json", Content: `{"key":1}`}, {Group: "G", Name: "yaml", Format: "yaml", Content: "key: 1\n"}, {Group: "G", Name: "toml", Format: "toml", Content: "key = 1"}, {Group: "G", Name: "xml", Format: "xml", Content: "<key>1</key>"}, {Group: "G", Name: "properties", Format: "properties", Content: "key=1"}, {Group: "G", Name: "ini", Format: "ini", Content: "[s]\nkey=1"}}
	data, err := configarchive.Encode(items)
	if err != nil {
		t.Fatal(err)
	}
	p, err := configarchive.Parse(data)
	if err != nil || !reflect.DeepEqual(p.Items, items) {
		t.Fatalf("formats: %v", err)
	}
	if _, err = configarchive.Parse(make([]byte, configarchive.MaxZIPBytes+1)); !errors.Is(err, config.ErrInvalid) {
		t.Fatal("upload cap", err)
	}
	if _, err = configarchive.Encode(make([]configarchive.Item, 1001)); !errors.Is(err, config.ErrInvalid) {
		t.Fatal("count cap", err)
	}
	large := make([]configarchive.Item, 65)
	for i := range large {
		large[i] = configarchive.Item{Group: "G", Name: fmt.Sprint(i), Format: "text", Content: strings.Repeat("a", 1<<20)}
	}
	if _, err = configarchive.Encode(large); !errors.Is(err, config.ErrInvalid) {
		t.Fatal("uncompressed cap", err)
	}
}

func TestLargeValidMetadataIsBoundedByWholeArchiveLimit(t *testing.T) {
	items := make([]configarchive.Item, 600)
	for i := range items {
		items[i] = configarchive.Item{Group: "G", Name: fmt.Sprint(i), Format: "text", Description: strings.Repeat("d", 4096)}
	}
	data, err := configarchive.Encode(items)
	if err != nil {
		t.Fatal(err)
	}
	p, err := configarchive.Parse(data)
	if err != nil || len(p.Items) != 600 {
		t.Fatal("valid large metadata rejected", err)
	}
}

func TestValidationErrorsDoNotContainConfigurationBodies(t *testing.T) {
	data := nacosZIP(t, [][2]string{{"G/app.ini", "[synthetic-secret-token"}, {".metadata.yml", "metadata:\n- {group: G, dataId: app.ini, type: ini}\n"}})
	_, err := configarchive.Parse(data)
	if err == nil {
		t.Fatal("invalid INI accepted")
	}
	if strings.Contains(err.Error(), "synthetic-secret-token") {
		t.Fatal("configuration body leaked in validation error")
	}
	var items *configarchive.ItemErrors
	if !errors.As(err, &items) {
		t.Fatal("missing structured item error")
	}
	for _, message := range items.Messages {
		if strings.Contains(message, "synthetic-secret-token") {
			t.Fatal("body leaked in item result")
		}
	}
}
