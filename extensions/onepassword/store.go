package main

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	op "github.com/1password/onepassword-sdk-go"
	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

type storeKey struct {
	priv             *ecdh.PrivateKey
	target           wire.SecretStoreTarget
	machine, session string
	until            time.Time
}
type storeValue struct {
	Name  string `json:"name"`
	Value []byte `json:"value"`
}

func storeFailure() error {
	return &ext.Error{Code: codeFailed, Message: "1Password storage failed; inspect item metadata before retrying"}
}
func storeInfo(key string) []byte { return []byte("persona secret store v1\x00" + key) }
func storeRandom() string {
	var b [24]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("random source unavailable")
	}
	return hex.EncodeToString(b[:])
}
func decodeStore(raw []byte, v any) error {
	if len(raw) > wire.SecretStoreSealedMost*2 {
		return storeFailure()
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		return storeFailure()
	}
	return nil
}

// store relays only public metadata/ciphertext to the selected resident node. SDK/service-account writes
// deliberately do not fall back here: the source encrypted to a key held only by that node process.
func (a *app) store(ctx context.Context, raw json.RawMessage) (any, error) {
	var in wire.SecretStoreParams
	if decodeStore(raw, &in) != nil || in.Target.Validate() != nil || (in.Contribution != "" && in.Contribution != providerID) {
		return nil, storeFailure()
	}
	a.mu.Lock()
	machine, call := a.machine, a.nodes
	a.mu.Unlock()
	if machine == "" || call == nil {
		return nil, &ext.Error{Code: "refused", Message: "secret writes require the configured resident op node"}
	}
	var out wire.SecretStoreResult
	if call(ctx, machine, "op.store", in, &out) != nil {
		return nil, storeFailure()
	}
	if in.Phase == "commit" {
		a.forgetMeta()
	}
	return out, nil
}

// storeNode runs with n.mu held, the same mutex/process as all existing op reads.
func (n *nodeApp) storeNode(ctx context.Context, raw json.RawMessage) (any, error) {
	var in wire.SecretStoreParams
	if decodeStore(raw, &in) != nil || in.Target.Validate() != nil || in.Machine == "" || in.Session == "" {
		return nil, storeFailure()
	}
	switch in.Phase {
	case "describe":
		if in.KeyID != "" || len(in.Sealed) != 0 {
			return nil, storeFailure()
		}
		t, vault, err := n.describeStore(ctx, in.Target)
		return wire.SecretStoreResult{Target: t, VaultName: vault}, err
	case "prepare":
		if in.KeyID != "" || len(in.Sealed) != 0 || !opID.MatchString(in.Target.Vault) || (in.Target.Operation == "edit" && !opID.MatchString(in.Target.Item)) {
			return nil, storeFailure()
		}
		if n.storeKeys == nil {
			n.storeKeys = map[string]storeKey{}
		}
		for id, k := range n.storeKeys {
			if time.Now().After(k.until) {
				delete(n.storeKeys, id)
			}
		}
		if len(n.storeKeys) >= 128 {
			return nil, storeFailure()
		}
		priv, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			return nil, storeFailure()
		}
		id := storeRandom()
		n.storeKeys[id] = storeKey{priv, in.Target, in.Machine, in.Session, time.Now().Add(2 * time.Minute)}
		return wire.SecretStoreResult{Target: in.Target, Key: base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()), KeyID: id}, nil
	case "commit":
		k, ok := n.storeKeys[in.KeyID]
		if !ok || k.machine != in.Machine || k.session != in.Session {
			return nil, storeFailure()
		}
		delete(n.storeKeys, in.KeyID) // every owner attempt consumes it, including malformed ciphertext
		want, _ := json.Marshal(k.target)
		got, _ := json.Marshal(in.Target)
		if time.Now().After(k.until) || !bytes.Equal(want, got) || len(in.Sealed) == 0 || len(in.Sealed) > wire.SecretStoreSealedMost {
			return nil, storeFailure()
		}
		sk, err := hpke.NewDHKEMPrivateKey(k.priv)
		if err != nil {
			return nil, storeFailure()
		}
		sealed, err := base64.StdEncoding.DecodeString(in.Sealed)
		if err != nil {
			return nil, storeFailure()
		}
		defer clear(sealed)
		plain, err := hpke.Open(sk, hpke.HKDFSHA256(), hpke.AES256GCM(), storeInfo(in.KeyID), sealed)
		if err != nil {
			return nil, storeFailure()
		}
		defer clear(plain)
		var vals []storeValue
		defer func() {
			for _, v := range vals {
				clear(v.Value)
			}
		}()
		if decodeStore(plain, &vals) != nil || len(vals) != len(k.target.Fields) {
			return nil, storeFailure()
		}
		for i, v := range vals {
			if v.Name != k.target.Fields[i].Name || len(v.Value) < 1 || len(v.Value) > wire.SecretStoreValueMost || !utf8.Valid(v.Value) || bytes.IndexByte(v.Value, 0) >= 0 {
				return nil, storeFailure()
			}
		}
		return n.writeStore(ctx, k.target, vals)
	}
	return nil, storeFailure()
}
func (n *nodeApp) describeStore(ctx context.Context, t wire.SecretStoreTarget) (wire.SecretStoreTarget, string, error) {
	vaults, err := n.vaults(ctx)
	if err != nil {
		return t, "", storeFailure()
	}
	v, ok := pick(vaults, t.Vault, func(v op.VaultOverview) (string, string) { return v.ID, v.Title })
	if !ok {
		return t, "", storeFailure()
	}
	t.Vault = v.ID
	if t.Operation == "edit" {
		if !opID.MatchString(t.Item) {
			return t, "", storeFailure()
		}
		it, err := n.item(ctx, t.Vault, t.Item)
		if err != nil {
			return t, "", storeFailure()
		}
		t.Title = it.Title
	}
	return t, v.Title, nil
}

// Full templates stay inside the node. Raw JSON preserves unrelated fields, sections, URLs and version.
// LOGIN edits are refused because CLI JSON templates can silently overwrite passkeys (official item docs).
func (n *nodeApp) writeStore(ctx context.Context, t wire.SecretStoreTarget, vals []storeValue) (any, error) {
	item := map[string]json.RawMessage{}
	put := func(m map[string]json.RawMessage, k string, v any) { m[k], _ = json.Marshal(v) }
	var fields []map[string]json.RawMessage
	args := []string{"item", "create", "--vault", t.Vault, "--format", "json", "--reveal"}
	if t.Operation == "edit" {
		raw, err := n.run(ctx, "item", "get", t.Item, "--vault", t.Vault, "--format", "json", "--reveal")
		if err != nil {
			return nil, storeFailure()
		}
		defer clear(raw)
		if json.Unmarshal(raw, &item) != nil {
			return nil, storeFailure()
		}
		var title, category, id string
		_ = json.Unmarshal(item["title"], &title)
		_ = json.Unmarshal(item["category"], &category)
		_ = json.Unmarshal(item["id"], &id)
		if title != t.Title || id != t.Item || strings.EqualFold(category, "LOGIN") {
			return nil, &ext.Error{Code: "refused", Message: "The item changed, or is a Login item: CLI JSON edits cannot safely preserve passkeys"}
		}
		for _, key := range []string{"passkeys", "files", "document"} {
			if b := bytes.TrimSpace(item[key]); len(b) > 0 && string(b) != "null" && string(b) != "[]" {
				return nil, &ext.Error{Code: "refused", Message: "Template writes cannot preserve this item's passkeys or attachments"}
			}
		}
		if json.Unmarshal(item["fields"], &fields) != nil {
			return nil, storeFailure()
		}
		args = []string{"item", "edit", t.Item, "--vault", t.Vault, "--format", "json", "--reveal"}
	} else {
		put(item, "title", t.Title)
		put(item, "category", t.Category)
		put(item, "vault", map[string]string{"id": t.Vault})
	}
	// Clear all owned encodings even on failure. Go strings/encoder copies remain subject to GC.
	defer func() {
		for _, b := range item {
			clear(b)
		}
		for _, f := range fields {
			for _, b := range f {
				clear(b)
			}
		}
	}()
	for i, v := range vals {
		match := -1
		for j, f := range fields {
			var label, id string
			_ = json.Unmarshal(f["label"], &label)
			_ = json.Unmarshal(f["id"], &id)
			if label == v.Name || id == v.Name {
				if match >= 0 {
					return nil, storeFailure()
				}
				match = j
			}
		}
		if match < 0 {
			fields = append(fields, map[string]json.RawMessage{})
			match = len(fields) - 1
			put(fields[match], "id", storeRandom()[:26])
			put(fields[match], "label", v.Name)
		}
		f := fields[match]
		if sec := bytes.TrimSpace(f["section"]); len(sec) > 0 && string(sec) != "null" && string(sec) != "{}" {
			return nil, &ext.Error{Code: "refused", Message: "Sectioned fields require an unambiguous field reference and are not supported for storage yet"}
		}
		clear(f["value"])
		put(f, "value", string(v.Value))
		put(f, "type", t.Fields[i].Type)
	}
	put(item, "fields", fields)
	template, err := json.Marshal(item)
	if err != nil {
		return nil, storeFailure()
	}
	defer clear(template)
	run := n.write
	if run == nil {
		run = runOpInput
	}
	out, err := run(ctx, template, args...)
	if err != nil {
		return nil, storeFailure()
	}
	defer clear(out)
	n.noteAuthorization(time.Now())
	var saved cliItem
	if json.Unmarshal(out, &saved) != nil || !opID.MatchString(saved.ID) || saved.Vault.ID != t.Vault {
		return nil, storeFailure()
	}
	refs := map[string]string{}
	for _, f := range t.Fields {
		id := ""
		for _, sf := range saved.Fields {
			if sf.Label == f.Name || sf.ID == f.Name {
				if id != "" {
					return nil, storeFailure()
				}
				id = sf.ID
			}
		}
		if id == "" || !refSafe(id) {
			return nil, storeFailure()
		}
		refs[f.Name] = "op://" + saved.Vault.ID + "/" + saved.ID + "/" + id
	}
	return wire.SecretStoreResult{Target: t, References: refs}, nil
}
