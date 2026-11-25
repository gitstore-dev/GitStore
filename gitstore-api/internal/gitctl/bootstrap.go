// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package gitctl

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Bootstrap preparation is offline and component-scoped. Neither command needs
// the other component's secrets, a running API, a shell, or a container runtime.
func runBootstrap(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "api" && args[0] != "controller") {
		fmt.Fprintln(stderr, "bootstrap requires api or controller")
		return 2
	}
	component := args[0]
	flags := flag.NewFlagSet("bootstrap "+component, flag.ContinueOnError)
	flags.SetOutput(stderr)
	keyPath := flags.String("private-key-path", "", "absolute component-owned PKCS#8 PEM path")
	uid := flags.Int("owner-uid", -1, "optional numeric owner of prepared files and newly created directories")
	gid := flags.Int("owner-gid", -1, "optional numeric group of prepared files and newly created directories")
	var recordPath, keyID string
	if component == "controller" {
		flags.StringVar(&recordPath, "record-output-path", "", "absolute controller-only signing-record path")
		flags.StringVar(&keyID, "key-id", "", "public key ID used for enrollment")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 || !filepath.IsAbs(*keyPath) || *uid < -1 || *gid < -1 ||
		(component == "controller" && (!filepath.IsAbs(recordPath) || strings.TrimSpace(keyID) == "" || len(keyID) > 128 || overlappingBootstrapPaths(*keyPath, recordPath))) {
		fmt.Fprintln(stderr, "bootstrap requires explicit absolute, distinct output paths and valid ownership/key ID options")
		return 2
	}
	if err := prepareBootstrap(*keyPath, recordPath, keyID, *uid, *gid); err != nil {
		// Paths, file contents and parser diagnostics may contain private material.
		fmt.Fprintln(stderr, "bootstrap could not prepare credentials; check paths, permissions, ownership and existing material (existing keys are never replaced)")
		return 1
	}
	fmt.Fprintln(stdout, "Component credentials prepared; existing keys preserved.")
	return 0
}

func overlappingBootstrapPaths(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	return a == b || strings.HasPrefix(a, b+string(os.PathSeparator)) || strings.HasPrefix(b, a+string(os.PathSeparator))
}

func prepareBootstrap(keyPath, recordPath, keyID string, uid, gid int) error {
	key, err := openBootstrapOutput(keyPath, uid, gid)
	if err != nil {
		return err
	}
	defer key.root.Close()
	key.tightenPermissions = true
	var record *bootstrapOutput
	var previousRecord []byte
	if recordPath != "" {
		record, err = openBootstrapOutput(recordPath, uid, gid)
		if err != nil {
			return err
		}
		defer record.root.Close()
		record.tightenPermissions = true
		previousRecord, err = record.read()
		defer clear(previousRecord)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	material, err := key.read()
	if errors.Is(err, os.ErrNotExist) {
		if previousRecord != nil {
			return errors.New("existing record requires its original private key")
		}
		_, signer, generateErr := ed25519.GenerateKey(rand.Reader)
		if generateErr != nil {
			return generateErr
		}
		material, _, _, err = encodeEnrollmentKeyPair(signer)
		if err != nil {
			return err
		}
		if err = key.publish(material, uid, gid); errors.Is(err, os.ErrExist) {
			// Concurrent preparation may have published the winning key first.
			clear(material)
			material, err = key.read()
		}
	}
	defer clear(material)
	if err != nil {
		return err
	}
	if _, err := parseEnrollmentPrivateKey(material); err != nil {
		return err
	}
	if record != nil {
		data, err := json.Marshal(struct {
			Format string            `json:"format"`
			Values map[string][]byte `json:"values"`
		}{"serviceaccount-signing-key/v1", map[string][]byte{"privateKey": material, "keyID": []byte(keyID)}})
		if err != nil {
			return err
		}
		defer clear(data)
		if err := record.ensure(data, uid, gid, equivalentSigningRecord); err != nil {
			return err
		}
	}
	return key.ownership(uid, gid)
}

type bootstrapOutput struct {
	root               *os.Root
	name               string
	tightenPermissions bool
}

func openBootstrapOutput(path string, uid, gid int) (*bootstrapOutput, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("absolute output path required")
	}
	path = filepath.Clean(path)
	if err := prepareBootstrapDirectory(filepath.Dir(path), uid, gid); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	return &bootstrapOutput{root: root, name: filepath.Base(path)}, nil
}

func prepareBootstrapDirectory(path string, uid, gid int) error {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return errors.New("output parent is not a directory")
		}
		return nil // Never change permissions/ownership of pre-existing directories.
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := prepareBootstrapDirectory(filepath.Dir(path), uid, gid); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return prepareBootstrapDirectory(path, uid, gid)
		}
		return err
	}
	if uid != -1 || gid != -1 {
		return os.Chown(path, uid, gid)
	}
	return nil
}

func (o *bootstrapOutput) open() (*os.File, error) {
	before, err := o.root.Lstat(o.name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || (!o.tightenPermissions && before.Mode().Perm()&0077 != 0) || before.Size() > 64*1024 {
		return nil, errors.New("existing output must be a bounded private regular file")
	}
	f, err := o.root.OpenFile(o.name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || (!o.tightenPermissions && after.Mode().Perm()&0077 != 0) {
		_ = f.Close()
		return nil, errors.New("output changed while opening")
	}
	return f, nil
}

func (o *bootstrapOutput) read() ([]byte, error) {
	f, err := o.open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		clear(data)
		return nil, errors.New("cannot read bounded output")
	}
	return data, nil
}

func (o *bootstrapOutput) ownership(uid, gid int) error {
	if uid == -1 && gid == -1 && !o.tightenPermissions {
		return nil
	}
	f, err := o.open()
	if err != nil {
		return err
	}
	defer f.Close()
	if uid != -1 || gid != -1 {
		if err := f.Chown(uid, gid); err != nil {
			return err
		}
	}
	if o.tightenPermissions {
		return f.Chmod(0600)
	}
	return nil
}

// Publish a complete, fsynced file without replacing any existing destination.
func (o *bootstrapOutput) publish(data []byte, uid, gid int) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	name := fmt.Sprintf(".bootstrap-%x", nonce)
	f, err := o.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer o.root.Remove(name)
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if uid != -1 || gid != -1 {
		if err := f.Chown(uid, gid); err != nil {
			return err
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := o.root.Link(name, o.name); err != nil {
		return err
	}
	return o.syncDirectory()
}

func (o *bootstrapOutput) syncDirectory() error {
	dir, err := o.root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// Replacement is explicit and used only for a validated identity file. Keys
// and signing records never use this path. Serialize enrollment per identity.
func (o *bootstrapOutput) replace(data []byte) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temporary := &bootstrapOutput{root: o.root, name: fmt.Sprintf(".identity-%x", nonce)}
	defer o.root.Remove(temporary.name)
	if err := temporary.publish(data, -1, -1); err != nil {
		return err
	}
	if err := o.root.Rename(temporary.name, o.name); err != nil {
		return err
	}
	return o.syncDirectory()
}

func (o *bootstrapOutput) ensure(data []byte, uid, gid int, equivalent func([]byte, []byte) bool) error {
	if equivalent == nil {
		equivalent = bytes.Equal
	}
	existing, err := o.read()
	if errors.Is(err, os.ErrNotExist) {
		if err = o.publish(data, uid, gid); !errors.Is(err, os.ErrExist) {
			return err
		}
		existing, err = o.read()
	}
	defer clear(existing)
	if err != nil || !equivalent(existing, data) {
		return errors.New("existing output differs; explicit recovery or rotation required")
	}
	return o.ownership(uid, gid)
}

// Accept older shell-generated records regardless of JSON field order, while
// retaining exact envelope validation (including duplicate-field rejection).
func equivalentSigningRecord(a, b []byte) bool {
	decode := func(data []byte) (map[string][]byte, error) {
		envelope, err := bootstrapObject(data)
		if err != nil || len(envelope) != 2 || string(envelope["format"]) != `"serviceaccount-signing-key/v1"` {
			return nil, errors.New("invalid signing record")
		}
		values, err := bootstrapObject(envelope["values"])
		if err != nil || len(values) != 2 {
			return nil, errors.New("invalid signing values")
		}
		result := make(map[string][]byte)
		for _, key := range []string{"privateKey", "keyID"} {
			var value []byte
			if err := json.Unmarshal(values[key], &value); err != nil || len(value) == 0 {
				clear(result["privateKey"])
				return nil, errors.New("invalid signing value")
			}
			result[key] = value
		}
		return result, nil
	}
	left, err := decode(a)
	if err != nil {
		return false
	}
	defer clear(left["privateKey"])
	right, err := decode(b)
	if err != nil {
		return false
	}
	defer clear(right["privateKey"])
	return bytes.Equal(left["privateKey"], right["privateKey"]) && bytes.Equal(left["keyID"], right["keyID"])
}

func bootstrapObject(data []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return nil, errors.New("expected object")
	}
	values := make(map[string]json.RawMessage)
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || values[key] != nil || len(values) == 2 {
			return nil, errors.New("invalid field")
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		values[key] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("trailing data")
	}
	return values, nil
}
