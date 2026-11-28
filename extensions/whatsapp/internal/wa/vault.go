package wa

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/connector"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
	"modernc.org/sqlite"
)

// vaultBlob is the JSON envelope stored inside the hub's encrypted connector_secrets store.
// The entire whatsmeow SQLite device database (identity keys, Signal sessions, prekeys, sender
// keys, app-state sync keys, and LID mappings) is serialized from an in-memory SQLite database
// and gzip-compressed here so that NO plaintext WhatsApp credentials or Signal keys ever touch disk.
type vaultBlob struct {
	Version    int    `json:"version"`
	Account    string `json:"account,omitempty"`
	JID        string `json:"jid,omitempty"`
	LID        string `json:"lid,omitempty"`
	PushName   string `json:"push_name,omitempty"`
	DeviceDBGZ []byte `json:"device_db_gz,omitempty"`
}

const (
	vaultVersion    = 1
	maxVaultRawSize = 32 << 20 // 32 MiB decompressed cap
)

// sqliteSerializer matches modernc.org/sqlite's conn.Serialize / conn.Deserialize methods.
type sqliteSerializer interface {
	Serialize() ([]byte, error)
	Deserialize([]byte) error
}

// DeviceVault hosts whatsmeow's sqlstore.Container purely in memory and persists snapshots
// exclusively to the hub's encrypted connector.Secrets store.
type DeviceVault struct {
	secrets connector.Secrets

	mu        sync.Mutex
	db        *sql.DB
	container *sqlstore.Container
	meta      vaultBlob
	dirty     atomic.Bool
	notify    chan struct{}
	closed    bool
}

// OpenDeviceVault initializes an in-memory SQLite device database, restores any previously saved
// encrypted state from secrets, upgrades the whatsmeow schema, and hooks SQLite commits so every
// key/session mutation marks the vault dirty for persistence.
func OpenDeviceVault(ctx context.Context, secrets connector.Secrets) (*DeviceVault, error) {
	v := &DeviceVault{
		secrets: secrets,
		notify:  make(chan struct{}, 1),
	}
	var raw []byte
	if secrets != nil {
		var err error
		raw, err = secrets.Load(ctx)
		if err != nil {
			return nil, fmt.Errorf("load encrypted WhatsApp session: %w", err)
		}
	}
	if len(raw) > 0 {
		var blob vaultBlob
		if err := json.Unmarshal(raw, &blob); err != nil {
			return nil, errors.New("stored WhatsApp session cannot be decoded")
		}
		v.meta = blob
	}
	if err := v.openDB(ctx, v.meta.DeviceDBGZ); err != nil {
		return nil, err
	}
	return v, nil
}

func (v *DeviceVault) openDB(ctx context.Context, compressed []byte) error {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return fmt.Errorf("open in-memory device sqlite: %w", err)
	}
	// Pin to a single persistent connection so the in-memory database lives for the lifetime of db.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if len(compressed) > 0 {
		plain, err := gunzipBounded(compressed, maxVaultRawSize)
		if err != nil {
			_ = db.Close()
			return fmt.Errorf("decompress stored WhatsApp device state: %w", err)
		}
		if err := deserializeDB(ctx, db, plain); err != nil {
			_ = db.Close()
			return fmt.Errorf("restore WhatsApp device state: %w", err)
		}
	}

	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return fmt.Errorf("enable sqlite foreign_keys: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		_ = db.Close()
		return fmt.Errorf("set sqlite busy_timeout: %w", err)
	}

	container := sqlstore.NewWithDB(db, "sqlite", waLog.Noop)
	if err := container.Upgrade(ctx); err != nil {
		_ = db.Close()
		return fmt.Errorf("upgrade whatsmeow device schema: %w", err)
	}

	// Register commit hook after upgrade so live key/session changes trigger persistence.
	if err := registerCommitHook(ctx, db, func() int32 {
		v.dirty.Store(true)
		select {
		case v.notify <- struct{}{}:
		default:
		}
		return 0
	}); err != nil {
		_ = db.Close()
		return fmt.Errorf("register sqlite commit hook: %w", err)
	}

	v.db = db
	v.container = container
	return nil
}

// Device returns the existing whatsmeow device if paired, or a fresh unpaired device ready to link.
func (v *DeviceVault) Device(ctx context.Context) (*store.Device, error) {
	v.mu.Lock()
	c := v.container
	v.mu.Unlock()
	if c == nil {
		return nil, errors.New("device vault is closed")
	}
	dev, err := c.GetFirstDevice(ctx)
	if err != nil {
		return nil, err
	}
	return dev, nil
}

// HasPairedDevice reports whether the vault currently contains a linked WhatsApp device.
func (v *DeviceVault) HasPairedDevice(ctx context.Context) bool {
	v.mu.Lock()
	c := v.container
	v.mu.Unlock()
	if c == nil {
		return false
	}
	devs, err := c.GetAllDevices(ctx)
	if err != nil || len(devs) == 0 {
		return false
	}
	for _, d := range devs {
		if d != nil && d.ID != nil && !d.ID.IsEmpty() {
			return true
		}
	}
	return false
}

// Meta returns the saved account metadata (Account, JID, LID, PushName).
func (v *DeviceVault) Meta() (account, jid, lid, pushName string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.meta.Account, v.meta.JID, v.meta.LID, v.meta.PushName
}

// SetMeta updates the account metadata stored alongside the encrypted device DB.
func (v *DeviceVault) SetMeta(account, jid, lid, pushName string) {
	v.mu.Lock()
	changed := v.meta.Account != account || v.meta.JID != jid || v.meta.LID != lid || v.meta.PushName != pushName
	v.meta.Account = account
	v.meta.JID = jid
	v.meta.LID = lid
	v.meta.PushName = pushName
	v.mu.Unlock()
	if changed {
		v.dirty.Store(true)
		select {
		case v.notify <- struct{}{}:
		default:
		}
	}
}

// NotifyChan fires whenever the in-memory device database commits changes.
func (v *DeviceVault) NotifyChan() <-chan struct{} {
	return v.notify
}

// Dirty reports whether uncommitted changes are waiting to be flushed to Hub secrets.
func (v *DeviceVault) Dirty() bool {
	return v.dirty.Load()
}

// Flush serializes the in-memory device SQLite DB, compresses it, and saves it to the hub's
// encrypted connector_secrets store. Does nothing if the device is not paired yet unless force is true.
func (v *DeviceVault) Flush(ctx context.Context, force bool) error {
	if v.secrets == nil {
		v.dirty.Store(false)
		return nil
	}
	if !force && !v.dirty.Load() {
		return nil
	}
	v.mu.Lock()
	if v.closed || v.db == nil {
		v.mu.Unlock()
		return nil
	}
	db := v.db
	meta := v.meta
	v.mu.Unlock()

	// Only persist once a paired device exists (avoid storing empty unpaired noise keys).
	if !v.HasPairedDevice(ctx) {
		v.dirty.Store(false)
		return nil
	}

	rawDB, err := serializeDB(ctx, db)
	if err != nil {
		return fmt.Errorf("serialize WhatsApp device state: %w", err)
	}
	gz, err := gzipBytes(rawDB)
	if err != nil {
		return fmt.Errorf("compress WhatsApp device state: %w", err)
	}
	blob := vaultBlob{
		Version:    vaultVersion,
		Account:    meta.Account,
		JID:        meta.JID,
		LID:        meta.LID,
		PushName:   meta.PushName,
		DeviceDBGZ: gz,
	}
	payload, err := json.Marshal(blob)
	if err != nil {
		return err
	}
	if err := v.secrets.Save(ctx, payload); err != nil {
		return fmt.Errorf("save encrypted WhatsApp session: %w", err)
	}
	v.dirty.Store(false)
	return nil
}

// RunFlusher flushes dirty device state to Hub secrets on a debounce interval until ctx ends,
// then performs one final flush.
func (v *DeviceVault) RunFlusher(ctx context.Context, debounce time.Duration) {
	if debounce <= 0 {
		debounce = 2 * time.Second
	}
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = v.Flush(fctx, false)
			cancel()
			return
		case <-v.notify:
			if timer == nil {
				timer = time.NewTimer(debounce)
				timerC = timer.C
			}
		case <-timerC:
			timer = nil
			timerC = nil
			if v.dirty.Load() {
				fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				_ = v.Flush(fctx, false)
				cancel()
			}
		}
	}
}

// Clear deletes the stored encrypted secret from the hub and replaces the in-memory device DB
// with a fresh empty database ready for a new login.
func (v *DeviceVault) Clear(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.secrets != nil {
		if err := v.secrets.Delete(ctx); err != nil {
			return fmt.Errorf("delete encrypted WhatsApp session: %w", err)
		}
	}
	if v.db != nil {
		_ = v.db.Close()
		v.db = nil
		v.container = nil
	}
	v.meta = vaultBlob{}
	v.dirty.Store(false)
	if v.closed {
		return nil
	}
	return v.openDB(ctx, nil)
}

// Close flushes pending state (if paired) and closes the in-memory database.
func (v *DeviceVault) Close(ctx context.Context) error {
	if v.dirty.Load() {
		_ = v.Flush(ctx, false)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.closed = true
	if v.db != nil {
		err := v.db.Close()
		v.db = nil
		v.container = nil
		return err
	}
	return nil
}

func serializeDB(ctx context.Context, db *sql.DB) ([]byte, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var out []byte
	err = conn.Raw(func(driverConn any) error {
		s, ok := driverConn.(sqliteSerializer)
		if !ok {
			return fmt.Errorf("sqlite driver %T does not support Serialize", driverConn)
		}
		var serr error
		out, serr = s.Serialize()
		return serr
	})
	return out, err
}

func deserializeDB(ctx context.Context, db *sql.DB, data []byte) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(driverConn any) error {
		s, ok := driverConn.(sqliteSerializer)
		if !ok {
			return fmt.Errorf("sqlite driver %T does not support Deserialize", driverConn)
		}
		return s.Deserialize(data)
	})
}

func registerCommitHook(ctx context.Context, db *sql.DB, fn sqlite.CommitHookFn) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(driverConn any) error {
		hr, ok := driverConn.(sqlite.HookRegisterer)
		if !ok {
			return fmt.Errorf("sqlite driver %T does not support commit hooks", driverConn)
		}
		hr.RegisterCommitHook(fn)
		return nil
	})
}

func gzipBytes(src []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(src); err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gunzipBounded(src []byte, limit int64) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	out, err := io.ReadAll(io.LimitReader(zr, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > limit {
		return nil, fmt.Errorf("decompressed device state exceeds %d bytes", limit)
	}
	return out, nil
}
