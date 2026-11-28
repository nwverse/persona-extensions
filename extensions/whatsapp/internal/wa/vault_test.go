package wa

import (
	"context"
	"sync"
	"testing"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/types"
)

type memSecrets struct {
	mu      sync.Mutex
	data    []byte
	saves   int
	deletes int
}

func (m *memSecrets) Load(context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		return nil, nil
	}
	return append([]byte(nil), m.data...), nil
}

func (m *memSecrets) Save(_ context.Context, d []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = append([]byte(nil), d...)
	m.saves++
	return nil
}

func (m *memSecrets) Delete(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = nil
	m.deletes++
	return nil
}

func TestDeviceVaultRoundTripInHubSecrets(t *testing.T) {
	ctx := context.Background()
	sec := &memSecrets{}

	// 1. Open fresh vault: no paired device, nothing persisted yet.
	v1, err := OpenDeviceVault(ctx, sec)
	if err != nil {
		t.Fatalf("OpenDeviceVault(fresh): %v", err)
	}
	if v1.HasPairedDevice(ctx) {
		t.Fatal("fresh vault should not have a paired device")
	}
	// Unpaired flush must not store noise keys in Hub secrets.
	if err := v1.Flush(ctx, true); err != nil {
		t.Fatalf("Flush(unpaired): %v", err)
	}
	if sec.saves != 0 {
		t.Fatalf("expected 0 saves before pairing, got %d", sec.saves)
	}

	// 2. Simulate pairing: populate device JID, LID, PushName and save to in-memory sqlstore.
	dev, err := v1.Device(ctx)
	if err != nil {
		t.Fatalf("Device: %v", err)
	}
	jid := types.NewADJID("12025550199", 0, 1)
	lid := types.NewJID("7766554433", types.HiddenUserServer)
	dev.ID = &jid
	dev.LID = lid
	dev.PushName = "Test User"
	dev.Account = &waAdv.ADVSignedDeviceIdentity{
		Details:             []byte("synthetic-details"),
		AccountSignature:    make([]byte, 64),
		AccountSignatureKey: make([]byte, 32),
		DeviceSignature:     make([]byte, 64),
	}
	if err := dev.Save(ctx); err != nil {
		t.Fatalf("dev.Save: %v", err)
	}
	if !v1.Dirty() {
		t.Fatal("expected SQLite commit hook to mark vault dirty after dev.Save")
	}
	wantRegID := dev.RegistrationID
	wantPub := *dev.IdentityKey.Pub

	v1.SetMeta("Test User (+12025550199)", "12025550199@s.whatsapp.net", "7766554433@lid", "Test User")
	if err := v1.Flush(ctx, true); err != nil {
		t.Fatalf("Flush(paired): %v", err)
	}
	if sec.saves != 1 || len(sec.data) == 0 {
		t.Fatalf("expected encrypted blob saved once, saves=%d len=%d", sec.saves, len(sec.data))
	}
	if err := v1.Close(ctx); err != nil {
		t.Fatalf("v1.Close: %v", err)
	}

	// 3. Reopen brand-new vault from the serialized Hub secret blob (simulating restart/upgrade).
	v2, err := OpenDeviceVault(ctx, sec)
	if err != nil {
		t.Fatalf("OpenDeviceVault(restore): %v", err)
	}
	defer v2.Close(ctx)
	if !v2.HasPairedDevice(ctx) {
		t.Fatal("restored vault should have paired device")
	}
	acc, gotJID, gotLID, gotPush := v2.Meta()
	if acc != "Test User (+12025550199)" || gotJID != "12025550199@s.whatsapp.net" || gotLID != "7766554433@lid" || gotPush != "Test User" {
		t.Fatalf("restored meta mismatch: acc=%q jid=%q lid=%q push=%q", acc, gotJID, gotLID, gotPush)
	}
	dev2, err := v2.Device(ctx)
	if err != nil {
		t.Fatalf("v2.Device: %v", err)
	}
	if dev2.ID == nil || dev2.ID.String() != jid.String() {
		t.Fatalf("restored device JID = %v, want %v", dev2.ID, jid)
	}
	if dev2.RegistrationID != wantRegID || *dev2.IdentityKey.Pub != wantPub {
		t.Fatal("restored Signal registration ID or identity key did not match")
	}
	// Verify restored in-memory DB is writable (SQLITE_DESERIALIZE_RESIZEABLE).
	dev2.PushName = "Renamed User"
	if err := dev2.Save(ctx); err != nil {
		t.Fatalf("dev2.Save after restore: %v", err)
	}

	// 4. Clear deletes Hub secret and yields a clean unpaired device.
	if err := v2.Clear(ctx); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if sec.deletes != 1 || len(sec.data) != 0 {
		t.Fatalf("expected Hub secret deleted, deletes=%d len=%d", sec.deletes, len(sec.data))
	}
	if v2.HasPairedDevice(ctx) {
		t.Fatal("vault should be unpaired after Clear")
	}
}
