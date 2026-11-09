// Package configarchive exchanges current configurations using Nacos's new ZIP format.
package configarchive

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/vincent-vinf/confhub/internal/config"
	"gopkg.in/yaml.v3"
)

const (
	MaxItems     = 1000
	MaxBytes     = 64 << 20
	MaxZIPBytes  = 8 << 20
	metadataName = ".metadata.yml"
)

type Item struct {
	Group       string `json:"group"`
	Name        string `json:"name"`
	Content     string `json:"content"`
	Format      string `json:"format"`
	Description string `json:"description"`
}
type Package struct {
	Digest string
	Items  []Item
}
type metadataItem struct {
	AppName     string `yaml:"appName"`
	DataID      string `yaml:"dataId"`
	Description string `yaml:"desc,omitempty"`
	Group       string `yaml:"group"`
	Type        string `yaml:"type"`
}
type metadata struct {
	Items []metadataItem `yaml:"metadata"`
}

func invalid(message string) error { return fmt.Errorf("%w: %s", config.ErrInvalid, message) }

// ItemErrors reports all invalid configurations without including their bodies.
// Structural archive errors remain ordinary errors because no reliable item list exists.
type ItemErrors struct{ Messages map[int]string }

func (e *ItemErrors) Error() string {
	for i := 0; i < MaxItems; i++ {
		if message, ok := e.Messages[i]; ok {
			return fmt.Sprintf("%v: %d invalid configurations; %s", config.ErrInvalid, len(e.Messages), message)
		}
	}
	return config.ErrInvalid.Error()
}
func (*ItemErrors) Unwrap() error { return config.ErrInvalid }

func Validate(items []Item) error {
	if len(items) == 0 || len(items) > MaxItems {
		return invalid("archive must contain 1–1000 configurations")
	}
	seen := map[string]bool{}
	check := func(item Item) error {
		key := item.Group + "/" + item.Name
		for _, name := range []string{item.Group, item.Name} {
			if err := config.ValidateName(name); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			if name == "." || name == ".." {
				return invalid("archive path cannot contain dot segments")
			}
		}
		if seen[key] {
			return invalid("duplicate configuration: " + key)
		}
		seen[key] = true
		if len(item.Description) > 4096 || !utf8.ValidString(item.Description) {
			return invalid("invalid description: " + key)
		}
		if err := config.ValidateContent(item.Format, item.Content); err != nil {
			return invalid(key + ": invalid content or unsupported format (UTF-8, at most 1 MiB)")
		}
		return nil
	}
	problems := &ItemErrors{Messages: map[int]string{}}
	size := 0
	for i, item := range items {
		size += len(item.Content)
		if size > MaxBytes {
			return invalid("archive exceeds 64 MiB uncompressed")
		}
		if err := check(item); err != nil {
			problems.Messages[i] = err.Error()
		}
	}
	if len(problems.Messages) > 0 {
		return problems
	}
	return nil
}

func Parse(data []byte) (Package, error) {
	var result Package
	if len(data) > MaxZIPBytes {
		return result, invalid("ZIP exceeds 8 MiB")
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return result, invalid("invalid ZIP")
	}
	if len(r.File) > MaxItems*3+1 {
		return result, invalid("too many ZIP entries")
	}
	files := map[string][]byte{}
	remaining := int64(MaxBytes)
	for _, f := range r.File {
		name := f.Name
		if strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
			return result, invalid("invalid ZIP path")
		}
		parts := strings.Split(strings.TrimSuffix(name, "/"), "/")
		for _, p := range parts {
			if p == "." || p == ".." || p == "" {
				return result, invalid("invalid ZIP path")
			}
		}
		if f.FileInfo().IsDir() {
			if len(parts) > 2 {
				return result, invalid("invalid directory path")
			}
			continue
		}
		if _, ok := files[name]; ok {
			return result, invalid("duplicate ZIP entry: " + name)
		}
		if name != metadataName && len(parts) != 2 {
			return result, invalid("expected group/dataId and .metadata.yml")
		}
		if f.UncompressedSize64 > uint64(remaining) {
			return result, invalid("archive exceeds 64 MiB uncompressed")
		}
		limit := int64(config.MaxContentBytes)
		if name == metadataName {
			limit = MaxBytes
		}
		if f.UncompressedSize64 > uint64(limit) {
			return result, invalid("oversized ZIP entry: " + name)
		}
		reader, e := f.Open()
		if e != nil {
			return result, invalid("cannot open ZIP entry")
		}
		content, e := io.ReadAll(io.LimitReader(reader, min(remaining, limit)+1))
		closeErr := reader.Close()
		if e != nil || closeErr != nil {
			return result, invalid("corrupt ZIP entry: " + name)
		}
		remaining -= int64(len(content))
		if remaining < 0 || int64(len(content)) > limit {
			return result, invalid("ZIP entry exceeds size limit")
		}
		if !utf8.Valid(content) {
			return result, invalid("ZIP entries must be UTF-8")
		}
		files[name] = content
	}
	meta, ok := files[metadataName]
	if !ok {
		return result, invalid("new Nacos .metadata.yml is required")
	}
	delete(files, metadataName)
	var m metadata
	decoder := yaml.NewDecoder(bytes.NewReader(meta))
	if err = decoder.Decode(&m); err != nil {
		return result, invalid("invalid .metadata.yml")
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return result, invalid("metadata must be one YAML document")
	}
	for _, item := range m.Items {
		name := item.Group + "/" + item.DataID
		body, exists := files[name]
		if !exists {
			return result, invalid("missing or duplicate configuration: " + name)
		}
		delete(files, name)
		result.Items = append(result.Items, Item{Group: item.Group, Name: item.DataID, Content: string(body), Format: item.Type, Description: item.Description})
	}
	if len(files) != 0 {
		return result, invalid("ZIP contains configuration not declared in metadata")
	}
	result.Digest = fmt.Sprintf("%x", sha256.Sum256(data))
	if err = Validate(result.Items); err != nil {
		return result, err
	}
	return result, nil
}

func Encode(items []Item) ([]byte, error) {
	if err := Validate(items); err != nil {
		return nil, err
	}
	var m metadata
	for _, item := range items {
		m.Items = append(m.Items, metadataItem{DataID: item.Name, Group: item.Group, Type: item.Format, Description: item.Description})
	}
	meta, err := yaml.Marshal(m)
	if err != nil {
		return nil, err
	}
	size := len(meta)
	for _, item := range items {
		size += len(item.Content)
	}
	if size > MaxBytes {
		return nil, invalid("archive exceeds 64 MiB size limit")
	}
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	write := func(name, content string) error {
		f, e := w.Create(name)
		if e != nil {
			return e
		}
		_, e = io.WriteString(f, content)
		return e
	}
	for _, item := range items {
		if err = write(item.Group+"/"+item.Name, item.Content); err != nil {
			return nil, err
		}
	}
	if err = write(metadataName, string(meta)); err != nil {
		return nil, err
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	if out.Len() > MaxZIPBytes {
		return nil, invalid("exported ZIP exceeds 8 MiB")
	}
	return out.Bytes(), nil
}
