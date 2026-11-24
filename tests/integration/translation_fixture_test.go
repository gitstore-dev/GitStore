// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"regexp"
	"slices"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

var translationIntegrationNamespace = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func translationIntegrationImage() ([]byte, error) {
	var content bytes.Buffer
	if err := png.Encode(&content, image.NewNRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		return nil, err
	}
	return content.Bytes(), nil
}

type translationIntegrationReference struct {
	APIVersion      string `json:"apiVersion" yaml:"apiVersion"`
	Kind            string `json:"kind" yaml:"kind"`
	Namespace       string `json:"namespace" yaml:"namespace"`
	Name            string `json:"name" yaml:"name"`
	UID             string `json:"uid" yaml:"uid"`
	ResourceVersion string `json:"resourceVersion" yaml:"resourceVersion"`
}

type translationIntegrationSpec struct {
	Locale    string                          `json:"locale" yaml:"locale"`
	TargetRef translationIntegrationReference `json:"targetRef" yaml:"targetRef"`
	Patches   []struct {
		FieldPath   string `json:"fieldPath" yaml:"fieldPath"`
		Translation string `json:"translation" yaml:"translation"`
	} `json:"patches" yaml:"patches"`
}

type translationIntegrationCondition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	ObservedGeneration int64  `json:"observedGeneration"`
}

type translationIntegrationResource struct {
	APIVersion string `json:"apiVersion" yaml:"apiVersion"`
	Kind       string `json:"kind" yaml:"kind"`
	Metadata   struct {
		Name            string `json:"name" yaml:"name"`
		Namespace       string `json:"namespace" yaml:"namespace"`
		UID             string `json:"uid"`
		ResourceVersion string `json:"resourceVersion"`
		Revision        string `json:"revision"`
		Generation      int64  `json:"generation"`
	} `json:"metadata" yaml:"metadata"`
	Spec   translationIntegrationSpec `json:"spec" yaml:"spec"`
	Body   *string                    `json:"body"`
	Status struct {
		ObservedGeneration  int64                             `json:"observedGeneration"`
		LastAppliedRevision string                            `json:"lastAppliedRevision"`
		Conditions          []translationIntegrationCondition `json:"conditions"`
		Resolved            *struct {
			CanonicalLocale string `json:"canonicalLocale"`
		} `json:"resolved"`
	} `json:"status"`
	body []byte
}

// Keep this oracle independent of the ingestion parser: body bytes are every
// byte after the first closing frontmatter line, including blank lines and CRLF.
func translationIntegrationDocument(source []byte) (header, body []byte, err error) {
	if len(source) > 1<<20 || !utf8.Valid(source) {
		return nil, nil, errors.New("translation fixture: invalid source size or UTF-8")
	}
	openingEnd := bytes.IndexByte(source, '\n')
	if openingEnd < 0 || !bytes.Equal(bytes.TrimSuffix(source[:openingEnd], []byte("\r")), []byte("---")) {
		return nil, nil, errors.New("translation fixture: missing opening frontmatter")
	}
	headerStart := openingEnd + 1
	for start := headerStart; start < len(source); {
		end := bytes.IndexByte(source[start:], '\n')
		next := len(source)
		if end < 0 {
			end = len(source)
		} else {
			end += start
			next = end + 1
		}
		if bytes.Equal(bytes.TrimSuffix(source[start:end], []byte("\r")), []byte("---")) {
			return source[headerStart:start], source[next:], nil
		}
		start = next
	}
	return nil, nil, errors.New("translation fixture: missing closing frontmatter")
}

func translationIntegrationSource(source []byte, oldNamespace, namespace string) ([]byte, error) {
	header, _, err := translationIntegrationDocument(source)
	if err != nil {
		return nil, err
	}
	if !translationIntegrationNamespace.MatchString(oldNamespace) || !translationIntegrationNamespace.MatchString(namespace) {
		return nil, errors.New("translation fixture: invalid namespace replacement")
	}
	var document yaml.Node
	if yaml.Unmarshal(header, &document) != nil || len(document.Content) != 1 {
		return nil, errors.New("translation fixture: invalid source frontmatter")
	}
	lookup := func(node *yaml.Node, key string) *yaml.Node {
		if node == nil || node.Kind != yaml.MappingNode {
			return nil
		}
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				return node.Content[i+1]
			}
		}
		return nil
	}
	root := document.Content[0]
	metadataNamespace := lookup(lookup(root, "metadata"), "namespace")
	if metadataNamespace == nil || metadataNamespace.Value != oldNamespace {
		return nil, errors.New("translation fixture: unexpected source namespace")
	}
	targetNamespace := lookup(lookup(lookup(root, "spec"), "targetRef"), "namespace")
	lines := bytes.SplitAfter(header, []byte("\n"))
	var offsets []int
	for _, node := range []*yaml.Node{metadataNamespace, targetNamespace} {
		if node == nil || node.Value != oldNamespace {
			continue
		}
		if node.Style != 0 || node.Tag != "!!str" || node.Line < 1 || node.Line > len(lines) || node.Column < 1 {
			return nil, errors.New("translation fixture: unsupported namespace scalar layout")
		}
		offset := node.Column - 1
		for _, line := range lines[:node.Line-1] {
			offset += len(line)
		}
		if offset+len(oldNamespace) > len(header) || string(header[offset:offset+len(oldNamespace)]) != oldNamespace {
			return nil, errors.New("translation fixture: namespace source position mismatch")
		}
		offsets = append(offsets, offset)
	}
	slices.Sort(offsets)
	replacement := bytes.Clone(header)
	for i := len(offsets) - 1; i >= 0; i-- {
		offset := offsets[i]
		replacement = bytes.Join([][]byte{replacement[:offset], []byte(namespace), replacement[offset+len(oldNamespace):]}, nil)
	}
	offset := bytes.IndexByte(source, '\n') + 1
	rendered := make([]byte, 0, len(source)-len(header)+len(replacement))
	rendered = append(rendered, source[:offset]...)
	rendered = append(rendered, replacement...)
	rendered = append(rendered, source[offset+len(header):]...)
	if len(rendered) > 1<<20 {
		return nil, errors.New("translation fixture: rendered source exceeds document budget")
	}
	return rendered, nil
}

func translationIntegrationExpected(source []byte) (translationIntegrationResource, error) {
	header, body, err := translationIntegrationDocument(source)
	if err != nil {
		return translationIntegrationResource{}, err
	}
	var resource translationIntegrationResource
	if yaml.Unmarshal(header, &resource) != nil || resource.Kind != "Translation" ||
		resource.APIVersion != "i18n.gitstore.dev/v1beta1" || resource.Metadata.Name == "" || resource.Metadata.Namespace == "" {
		return translationIntegrationResource{}, errors.New("translation fixture: invalid expected Translation")
	}
	resource.body = bytes.Clone(body)
	return resource, nil
}

func verifyTranslationIntegrationAuthored(source []byte, revision string, actual translationIntegrationResource) error {
	expected, err := translationIntegrationExpected(source)
	if err != nil {
		return err
	}
	if revision == "" || actual.APIVersion != expected.APIVersion || actual.Kind != expected.Kind ||
		actual.Metadata.Name != expected.Metadata.Name || actual.Metadata.Namespace != expected.Metadata.Namespace ||
		actual.Metadata.UID == "" || actual.Metadata.ResourceVersion == "" || actual.Metadata.Generation < 1 ||
		actual.Metadata.Revision != revision {
		return errors.New("translation fixture: public source identity or observation is not current")
	}
	if actual.Spec.Locale != expected.Spec.Locale || actual.Spec.TargetRef != expected.Spec.TargetRef ||
		len(actual.Spec.Patches) != len(expected.Spec.Patches) {
		return errors.New("translation fixture: public spec differs from authored source")
	}
	for i := range expected.Spec.Patches {
		if actual.Spec.Patches[i] != expected.Spec.Patches[i] {
			return errors.New("translation fixture: public patch differs from authored source")
		}
	}
	var body []byte
	if actual.Body != nil {
		body = []byte(*actual.Body)
	}
	if !bytes.Equal(body, expected.body) {
		return errors.New("translation fixture: public body differs from authored bytes")
	}
	return nil
}

// Target binding and durable watch evidence are separate from this source and
// freshness check; a matching read alone does not establish connected acceptance.
func verifyTranslationIntegrationResource(source []byte, revision string, actual translationIntegrationResource, ready bool) error {
	if err := verifyTranslationIntegrationAuthored(source, revision, actual); err != nil {
		return err
	}
	if actual.Status.LastAppliedRevision != revision || actual.Status.ObservedGeneration != actual.Metadata.Generation {
		return errors.New("translation fixture: public source identity or observation is not current")
	}
	status := "FALSE"
	if ready {
		status = "TRUE"
	}
	found := false
	for _, condition := range actual.Status.Conditions {
		if condition.Type != "Ready" {
			continue
		}
		if found || condition.Status != status || condition.ObservedGeneration != actual.Metadata.Generation {
			return errors.New("translation fixture: invalid current Ready condition")
		}
		found = true
	}
	if !found {
		return errors.New("translation fixture: missing current Ready condition")
	}
	return nil
}
