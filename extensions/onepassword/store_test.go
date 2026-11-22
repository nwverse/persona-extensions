package main

import (
	"context"
	"crypto/ecdh"
	"crypto/hpke"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
)

func storeCall(t *testing.T, n *nodeApp, in wire.SecretStoreParams) (wire.SecretStoreResult, error) {
	t.Helper()
	b, _ := json.Marshal(in)
	got, err := n.handle(context.Background(), "store", b)
	if err != nil {
		return wire.SecretStoreResult{}, err
	}
	raw, _ := json.Marshal(got)
	var out wire.SecretStoreResult
	err = json.Unmarshal(raw, &out)
	return out, err
}
func storeSeal(t *testing.T, key wire.SecretStoreResult, values []storeValue) string {
	t.Helper()
	raw, _ := base64.StdEncoding.DecodeString(key.Key)
	pub, _ := ecdh.P256().NewPublicKey(raw)
	pk, _ := hpke.NewDHKEMPublicKey(pub)
	plain, _ := json.Marshal(values)
	defer clear(plain)
	out, err := hpke.Seal(pk, hpke.HKDFSHA256(), hpke.AES256GCM(), storeInfo(key.KeyID), plain)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(out)
}
func storeTarget() wire.SecretStoreTarget {
	return wire.SecretStoreTarget{Operation: "create", Vault: fixVault, Title: "Synthetic test", Category: "API_CREDENTIAL", Fields: []wire.SecretStoreField{{Name: "credential", Type: "CONCEALED"}}}
}
func TestStorageCreateEditOnlyStdinAndReferenceAnswers(t *testing.T) {
	const secret = "FAKE-WRITE-VALUE-98181"
	n := &nodeApp{}
	var stored map[string]any
	writes := 0
	n.write = func(_ context.Context, input []byte, args ...string) ([]byte, error) {
		writes++
		if strings.Contains(strings.Join(args, " "), secret) {
			t.Fatal("value in argv")
		}
		if json.Unmarshal(input, &stored) != nil {
			t.Fatal("invalid stdin template")
		}
		fs := stored["fields"].([]any)
		if fs[len(fs)-1].(map[string]any)["value"] != secret {
			t.Fatal("stdin did not contain exact field value")
		}
		stored["id"] = fixItem
		stored["vault"] = map[string]string{"id": fixVault}
		return json.Marshal(stored)
	}
	in := wire.SecretStoreParams{Phase: "prepare", Target: storeTarget(), Machine: "source", Session: "writer"}
	key, err := storeCall(t, n, in)
	if err != nil {
		t.Fatal(err)
	}
	in.Phase = "commit"
	in.KeyID = key.KeyID
	in.Sealed = storeSeal(t, key, []storeValue{{Name: "credential", Value: []byte(secret)}})
	out, err := storeCall(t, n, in)
	if err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(out)
	if strings.Contains(string(public), secret) || !strings.HasPrefix(out.References["credential"], "op://"+fixVault+"/"+fixItem+"/") {
		t.Fatal("unsafe result")
	}
	if _, err = storeCall(t, n, in); err == nil || writes != 1 {
		t.Fatal("replay wrote")
	}
	// Edit preserves unrelated values, URLs and version entirely within the node,
	// including the CLI's unnamed default section used for ordinary custom fields.
	stored["fields"].([]any)[0].(map[string]any)["section"] = map[string]string{"id": "add more"}
	stored["sections"] = []any{map[string]any{"id": "add more", "label": ""}}
	stored["fields"] = append([]any{map[string]any{"id": "untouched", "label": "untouched", "type": "STRING", "value": "OTHER-FAKE-VALUE"}}, stored["fields"].([]any)...)
	stored["version"] = float64(7)
	stored["urls"] = []any{map[string]any{"href": "https://example.com"}}
	before, _ := json.Marshal(stored)
	n.run = func(context.Context, ...string) ([]byte, error) { return append([]byte{}, before...), nil }
	in = wire.SecretStoreParams{Phase: "prepare", Target: storeTarget(), Machine: "source", Session: "writer"}
	in.Target.Operation = "edit"
	in.Target.Item = fixItem
	in.Target.Category = ""
	key, err = storeCall(t, n, in)
	if err != nil {
		t.Fatal(err)
	}
	in.Phase = "commit"
	in.KeyID = key.KeyID
	in.Sealed = storeSeal(t, key, []storeValue{{Name: "credential", Value: []byte(secret)}})
	if _, err = storeCall(t, n, in); err != nil {
		t.Fatal(err)
	}
	if stored["version"] != float64(7) || stored["fields"].([]any)[0].(map[string]any)["value"] != "OTHER-FAKE-VALUE" || len(stored["urls"].([]any)) != 1 {
		t.Fatal("unrelated metadata or fields lost")
	}
	stored["sections"].([]any)[0].(map[string]any)["label"] = "Named section"
	before, _ = json.Marshal(stored)
	if _, err = n.writeStore(context.Background(), in.Target, []storeValue{{Name: "credential", Value: []byte(secret)}}); err == nil || writes != 2 {
		t.Fatal("named section was silently edited")
	}

}
func TestStorageRejectsChangedExpiredForeignAndInvalidCiphertext(t *testing.T) {
	for _, change := range []string{"target", "session", "expired", "ciphertext", "field"} {
		t.Run(change, func(t *testing.T) {
			writes := 0
			n := &nodeApp{write: func(context.Context, []byte, ...string) ([]byte, error) {
				writes++
				return nil, errors.New("must never run")
			}}
			in := wire.SecretStoreParams{Phase: "prepare", Target: storeTarget(), Machine: "source", Session: "writer"}
			key, err := storeCall(t, n, in)
			if err != nil {
				t.Fatal(err)
			}
			vals := []storeValue{{Name: "credential", Value: []byte("synthetic-payload")}}
			if change == "field" {
				vals[0].Name = "not-approved"
			}
			in.Phase = "commit"
			in.KeyID = key.KeyID
			in.Sealed = storeSeal(t, key, vals)
			switch change {
			case "target":
				in.Target.Title = "Different"
			case "session":
				in.Session = "other"
			case "expired":
				k := n.storeKeys[key.KeyID]
				k.until = time.Now().Add(-time.Second)
				n.storeKeys[key.KeyID] = k
			case "ciphertext":
				in.Sealed = "invalid"
			}
			if _, err = storeCall(t, n, in); err == nil || writes != 0 {
				t.Fatal("unapproved write accepted")
			}
		})
	}
}
func TestStorageHidesOpErrorsAndRefusesLossyLoginEdits(t *testing.T) {
	n := &nodeApp{write: func(context.Context, []byte, ...string) ([]byte, error) {
		return nil, errors.New("SYNTHETIC-SECRET-IN-STDERR")
	}}
	_, err := n.writeStore(context.Background(), storeTarget(), []storeValue{{Name: "credential", Value: []byte("fake")}})
	if err == nil || strings.Contains(err.Error(), "SYNTHETIC-SECRET") {
		t.Fatal("unsafe error")
	}
	n.run = func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"id":"` + fixItem + `","title":"Synthetic test","category":"LOGIN","fields":[]}`), nil
	}
	target := storeTarget()
	target.Operation = "edit"
	target.Item = fixItem
	target.Category = ""
	if _, err = n.writeStore(context.Background(), target, []storeValue{{Name: "credential", Value: []byte("fake")}}); err == nil {
		t.Fatal("potential passkey loss accepted")
	}
}
func TestKeepaliveStopsAtHardLimitAndAfterFailure(t *testing.T) {
	n := &nodeApp{}
	now := time.Now()
	if n.keepaliveDue(now) {
		t.Fatal("unauthorized session kept alive")
	}
	n.noteAuthorization(now)
	if n.keepaliveDue(now.Add(4*time.Minute)) || !n.keepaliveDue(now.Add(6*time.Minute)) || n.keepaliveDue(now.Add(12*time.Hour)) {
		t.Fatal("wrong idle/hard boundaries")
	}
	n.keepaliveBlocked = true
	if n.keepaliveDue(now.Add(6 * time.Minute)) {
		t.Fatal("failed session repeatedly prompted")
	}
	n.noteAuthorization(now.Add(13 * time.Hour))
	if !n.keepaliveDue(now.Add(13*time.Hour + 6*time.Minute)) {
		t.Fatal("foreground reauthorization did not restart keepalive")
	}
}
