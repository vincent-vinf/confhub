package config

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/magiconair/properties"
	"github.com/pelletier/go-toml/v2"
	"gopkg.in/ini.v1"
	"gopkg.in/yaml.v3"
)

func ValidateContent(format, content string) error {
	if len(content) > MaxContentBytes || !utf8.ValidString(content) {
		return fmt.Errorf("%w: content must be UTF-8 and at most 1 MiB", ErrInvalid)
	}
	var err error
	switch format {
	case "text":
		return nil
	case "json":
		if !json.Valid([]byte(content)) {
			err = fmt.Errorf("invalid JSON")
		}
	case "yaml":
		d := yaml.NewDecoder(strings.NewReader(content))
		for {
			var v any
			e := d.Decode(&v)
			if e == io.EOF {
				break
			}
			if e != nil {
				err = e
				break
			}
		}
	case "toml":
		var v any
		err = toml.Unmarshal([]byte(content), &v)
	case "xml":
		d := xml.NewDecoder(strings.NewReader(content))
		depth, roots := 0, 0
		for {
			token, e := d.Token()
			if e == io.EOF {
				break
			}
			if e != nil {
				err = e
				break
			}
			switch v := token.(type) {
			case xml.StartElement:
				if depth == 0 {
					roots++
				}
				depth++
			case xml.EndElement:
				depth--
			case xml.CharData:
				if depth == 0 && strings.TrimSpace(string(v)) != "" {
					err = fmt.Errorf("text outside XML root")
				}
			}
		}
		if err == nil && (roots != 1 || depth != 0) {
			err = fmt.Errorf("XML requires one root element")
		}
	case "ini":
		_, err = ini.LoadSources(ini.LoadOptions{}, []byte(content))
	case "properties":
		loader := properties.Loader{Encoding: properties.UTF8, DisableExpansion: true}
		_, err = loader.LoadBytes([]byte(content))
	default:
		err = fmt.Errorf("unknown format %q", format)
	}
	if err != nil {
		return fmt.Errorf("%w: %s syntax: %v", ErrInvalid, format, err)
	}
	return nil
}
