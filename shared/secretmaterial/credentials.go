// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secretmaterial

func validateCredentialMaterial(material SecretMaterial) error {
	for _, key := range []string{"accessKeyId", "secretAccessKey"} {
		if len(material.values[key]) == 0 {
			return failure(ErrMissingKey, "required-item", nil)
		}
	}
	return nil
}
