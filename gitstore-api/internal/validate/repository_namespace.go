// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package validate

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

func (p *Parser) namespaceForManifest(frontmatter []byte) (string, error) {
	if p.repositoryNamespace == nil {
		return "", nil
	}
	var envelope map[string]any
	if err := yaml.Unmarshal(frontmatter, &envelope); err != nil {
		return "", fmt.Errorf("validate: malformed YAML: %w", err)
	}
	switch envelope["kind"] {
	case "Product", "CategoryTaxonomy", "Collection", "ProductVariant", "Repository", "File", "Translation":
	default:
		return "", nil
	}
	meta, _ := envelope["metadata"].(map[string]any)
	value, present := meta["namespace"]
	authored, valid := value.(string)
	if present && (!valid || strings.TrimSpace(authored) == "") {
		return "", fmt.Errorf("validate: metadata.namespace NAMESPACE_REQUIRED: omit metadata.namespace or supply the owning namespace as a nonblank string")
	}
	effective, err := p.repositoryNamespace()
	if err != nil {
		return "", fmt.Errorf("validate: resolve repository namespace: %w", err)
	}
	if strings.TrimSpace(effective) == "" {
		return "", fmt.Errorf("validate: repository namespace is unavailable")
	}
	if present && authored != effective {
		return "", fmt.Errorf("validate: metadata.namespace must match the owning repository namespace")
	}
	return effective, nil
}
