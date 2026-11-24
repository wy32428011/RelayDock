package state

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/zalando/go-keyring"
	"remoteinstaller.local/core/internal/rpc"
)

// Host contains only display and pinning metadata, never a secret or local key path.
type Host struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Address         string `json:"address"`
	Port            int    `json:"port"`
	Username        string `json:"username"`
	Group           string `json:"group"`
	Auth            string `json:"auth"`
	OS              string `json:"os"`
	OSVersion       string `json:"osVersion"`
	Arch            string `json:"arch"`
	Fingerprint     string `json:"fingerprint"`
	LastSeen        string `json:"lastSeen,omitempty"`
	CredentialReady bool   `json:"credentialReady"`
}

// Credential stays inside the Go core and platform vault; it is never an RPC result.
type Credential struct{ Secret, KeyPath string }

type hostRow struct {
	Host
	ref string
}

const hostColumns = `id, name, address, port, username, group_name, auth, os, os_version, arch, fingerprint, last_seen, credential_ref`

func scanHost(row scanner) (hostRow, error) {
	var h hostRow
	err := row.Scan(&h.ID, &h.Name, &h.Address, &h.Port, &h.Username, &h.Group, &h.Auth, &h.OS, &h.OSVersion, &h.Arch, &h.Fingerprint, &h.LastSeen, &h.ref)
	return h, err
}

func getHostRow(ctx context.Context, q queryer, id string) (hostRow, error) {
	return scanHost(q.QueryRowContext(ctx, `SELECT `+hostColumns+` FROM hosts WHERE id=?`, id))
}

func writeHost(ctx context.Context, tx *sql.Tx, h hostRow) error {
	result, err := tx.ExecContext(ctx, `INSERT INTO hosts (`+hostColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,address=excluded.address,port=excluded.port,
		username=excluded.username,group_name=excluded.group_name,auth=excluded.auth,os=excluded.os,
		os_version=excluded.os_version,arch=excluded.arch,fingerprint=excluded.fingerprint,
		last_seen=excluded.last_seen,credential_ref=excluded.credential_ref`,
		h.ID, h.Name, h.Address, h.Port, h.Username, h.Group, h.Auth, h.OS, h.OSVersion, h.Arch, h.Fingerprint, h.LastSeen, h.ref)
	return requireOne(result, err)
}

func validText(value string, maxBytes int, multiline bool) bool {
	if len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, c := range value {
		if unicode.IsControl(c) && !(multiline && (c == '\n' || c == '\r' || c == '\t')) {
			return false
		}
	}
	return true
}

func validHost(h Host) bool {
	return validID(h.ID) && strings.TrimSpace(h.Name) != "" && validText(h.Name, 256, false) &&
		h.Address != "" && validText(h.Address, 512, false) && !strings.ContainsAny(h.Address, "/\\@?#") &&
		strings.IndexFunc(h.Address, unicode.IsSpace) < 0 && h.Port >= 1 && h.Port <= 65535 &&
		h.Username != "" && validText(h.Username, 256, false) && validText(h.Group, 256, false) &&
		(h.Auth == "password" || h.Auth == "key") && validText(h.OS, 80, false) &&
		validText(h.OSVersion, 160, false) && validText(h.Arch, 80, false) &&
		validText(h.Fingerprint, 256, false) && validText(h.LastSeen, 64, false)
}

func sameEndpoint(a, b Host) bool {
	return a.Address == b.Address && a.Port == b.Port && a.Username == b.Username && a.Auth == b.Auth
}

func usableCredential(auth string, cred Credential) bool {
	if len(cred.Secret) > 16*1024 {
		return false
	}
	if auth == "password" {
		return cred.Secret != "" && cred.KeyPath == ""
	}
	if auth != "key" || !filepath.IsAbs(cred.KeyPath) || !validText(cred.KeyPath, 4096, false) {
		return false
	}
	fi, err := os.Stat(cred.KeyPath)
	return err == nil && fi.Mode().IsRegular()
}

func missingCredential() error {
	return rpc.Error("AUTH_REQUIRED", "主机没有可用凭据，请重新录入或选择本机私钥")
}

func (s *Store) credentialFor(h hostRow) (Credential, error) {
	if c, ok := s.session[h.ID]; ok && usableCredential(h.Auth, c) {
		return c, nil
	}
	if !validRef(h.ref) {
		return Credential{}, missingCredential()
	}
	stored, err := s.vault.Get(s.service, h.ref)
	if errors.Is(err, keyring.ErrNotFound) {
		return Credential{}, missingCredential()
	}
	if err != nil {
		return Credential{}, rpc.Retry("CREDENTIAL_STORE_UNAVAILABLE", "系统凭据库不可用，请解锁后重试，或取消记住凭据并重新录入")
	}
	var c Credential
	if json.Unmarshal([]byte(stored), &c) != nil || !usableCredential(h.Auth, c) {
		return Credential{}, missingCredential()
	}
	return c, nil
}

func (s *Store) Host(ctx context.Context, id string) (Host, error) {
	if err := acquire(ctx, s.secretGate); err != nil {
		return Host{}, err
	}
	defer func() { <-s.secretGate }()
	h, err := getHostRow(ctx, s.DB, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Host{}, rpc.Error("HOST_NOT_FOUND", "此主机不存在或已删除")
	}
	if err != nil {
		return Host{}, err
	}
	_, err = s.credentialFor(h)
	h.CredentialReady = err == nil
	return h.Host, nil
}

func (s *Store) Credential(ctx context.Context, id string) (Credential, error) {
	if err := acquire(ctx, s.secretGate); err != nil {
		return Credential{}, err
	}
	defer func() { <-s.secretGate }()
	h, err := getHostRow(ctx, s.DB, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Credential{}, rpc.Error("HOST_NOT_FOUND", "此主机不存在或已删除")
	}
	if err != nil {
		return Credential{}, err
	}
	return s.credentialFor(h)
}

func (s *Store) listHosts(ctx context.Context) ([]Host, error) {
	if err := acquire(ctx, s.secretGate); err != nil {
		return nil, err
	}
	defer func() { <-s.secretGate }()
	rows, err := s.DB.QueryContext(ctx, `SELECT `+hostColumns+` FROM hosts ORDER BY group_name COLLATE NOCASE, name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []hostRow{}
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Host, 0, len(items))
	for _, h := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		_, err := s.credentialFor(h)
		h.CredentialReady = err == nil
		out = append(out, h.Host)
	}
	return out, nil
}

// CredentialRemembered reports the persistence choice without consulting the vault.
func (s *Store) CredentialRemembered(ctx context.Context, id string) (bool, error) {
	h, err := getHostRow(ctx, s.DB, id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, rpc.Error("HOST_NOT_FOUND", "此主机不存在或已删除")
	}
	return validRef(h.ref), err
}

// CredentialForHost refuses to load credentials that no longer belong to the
// endpoint and key pin already checked by the caller.
func (s *Store) CredentialForHost(ctx context.Context, expected Host) (Credential, error) {
	if err := acquire(ctx, s.secretGate); err != nil {
		return Credential{}, err
	}
	defer func() { <-s.secretGate }()
	h, err := getHostRow(ctx, s.DB, expected.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return Credential{}, rpc.Error("HOST_NOT_FOUND", "此主机不存在或已删除")
	}
	if err != nil {
		return Credential{}, err
	}
	if !sameEndpoint(h.Host, expected) || h.Fingerprint != expected.Fingerprint {
		return Credential{}, rpc.Error("HOST_CONFLICT", "主机连接信息已变化，请重新选择并测试")
	}
	return s.credentialFor(h)
}

func (s *Store) SaveHost(ctx context.Context, h Host, cred Credential, remember bool) (Host, error) {
	return s.saveHost(ctx, h, cred, remember, nil, false)
}

// SaveHostIfCurrent commits a probed host only if the metadata observed before
// probing still matches, or the fresh ID is unused for a new host.
func (s *Store) SaveHostIfCurrent(ctx context.Context, h Host, cred Credential, remember bool, expected *Host) (Host, error) {
	return s.saveHost(ctx, h, cred, remember, expected, true)
}

func (s *Store) saveHost(ctx context.Context, h Host, cred Credential, remember bool, expected *Host, cas bool) (Host, error) {
	if h.ID == "" {
		h.ID = rpc.ID()
	}
	h.Name, h.Address, h.Username, h.Group = strings.TrimSpace(h.Name), strings.TrimSpace(h.Address), strings.TrimSpace(h.Username), strings.TrimSpace(h.Group)
	if !validHost(h) {
		return Host{}, rpc.Error("BAD_PARAMS", "主机资料无效")
	}
	if !usableCredential(h.Auth, cred) {
		return Host{}, missingCredential()
	}
	if err := acquire(ctx, s.secretGate); err != nil {
		return Host{}, err
	}
	defer func() { <-s.secretGate }()
	defer s.collectSecrets(ctx)
	newRow := hostRow{Host: h}
	if remember {
		encoded, err := json.Marshal(cred)
		if err != nil {
			return Host{}, err
		}
		newRow.ref, err = s.stageSecret(ctx, string(encoded), true)
		if err != nil {
			return Host{}, err
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Host{}, err
	}
	defer tx.Rollback()
	old, err := getHostRow(ctx, tx, h.ID)
	missing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !missing {
		return Host{}, err
	}
	if cas {
		matches := missing && expected == nil
		if !missing && expected != nil {
			want := *expected
			want.CredentialReady = false
			matches = old.Host == want
		}
		if !matches {
			return Host{}, rpc.Error("HOST_CONFLICT", "主机资料已变化，请重新测试后保存")
		}
	}
	if err := writeHost(ctx, tx, newRow); err != nil {
		return Host{}, err
	}
	if err := swapRefs(ctx, tx, old.ref, newRow.ref); err != nil {
		return Host{}, err
	}
	if err := tx.Commit(); err != nil {
		return Host{}, err
	}
	delete(s.session, h.ID)
	if !remember {
		s.session[h.ID] = cred
	}
	h.CredentialReady = true
	return h, nil
}

func (s *Store) deleteHost(ctx context.Context, id string) error {
	if err := acquire(ctx, s.secretGate); err != nil {
		return err
	}
	defer func() { <-s.secretGate }()
	defer s.collectSecrets(ctx)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old, err := getHostRow(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		delete(s.session, id)
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hosts WHERE id=?`, id); err != nil {
		return err
	}
	if err := swapRefs(ctx, tx, old.ref, ""); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	delete(s.session, id)
	return nil
}

func validRef(ref string) bool {
	if len(ref) != 32 || strings.ToLower(ref) != ref {
		return false
	}
	_, err := hex.DecodeString(ref)
	return err == nil
}

// Each immutable vault entry is journaled before creation. A commit binds the
// new reference and queues the previous one in the same SQL transaction.
func (s *Store) stageSecret(ctx context.Context, secret string, host bool) (string, error) {
	ref := rpc.ID()
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO vault_gc(ref) VALUES(?)`, ref); err != nil {
		return "", err
	}
	if err := s.vault.Set(s.service, ref, secret); err == nil {
		if value, err := s.vault.Get(s.service, ref); err == nil && value == secret {
			return ref, nil
		}
	}
	if host {
		return "", rpc.Retry("CREDENTIAL_STORE_UNAVAILABLE", "无法写入系统凭据库；请解锁后重试，或取消记住凭据，仅在本次会话使用")
	}
	return "", rpc.Retry("CREDENTIAL_STORE_UNAVAILABLE", "无法写入系统凭据库；未保存设置，请解锁后重试或不填写新凭据")
}

func swapRefs(ctx context.Context, tx *sql.Tx, oldRef, newRef string) error {
	if oldRef != newRef && validRef(oldRef) {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO vault_gc(ref) VALUES(?)`, oldRef); err != nil {
			return err
		}
	}
	if validRef(newRef) {
		_, err := tx.ExecContext(ctx, `DELETE FROM vault_gc WHERE ref=?`, newRef)
		return err
	}
	return nil
}

func (s *Store) collectSecrets(ctx context.Context) {
	rows, err := s.DB.QueryContext(ctx, `SELECT g.ref FROM vault_gc g
		WHERE NOT EXISTS(SELECT 1 FROM hosts h WHERE h.credential_ref=g.ref)
		  AND NOT EXISTS(SELECT 1 FROM preference_credentials p WHERE p.ref=g.ref)
		LIMIT 64`)
	if err != nil {
		return
	}
	var refs []string
	for rows.Next() {
		var ref string
		if rows.Scan(&ref) != nil {
			_ = rows.Close()
			return
		}
		refs = append(refs, ref)
	}
	if err := rows.Close(); err != nil || rows.Err() != nil {
		return
	}
	for _, ref := range refs {
		if ctx.Err() != nil {
			return
		}
		if validRef(ref) {
			if err := s.vault.Delete(s.service, ref); err != nil && !errors.Is(err, keyring.ErrNotFound) {
				return // Leave the pending references alone until the vault is accessible.
			}
		}
		_, _ = s.DB.ExecContext(ctx, `DELETE FROM vault_gc WHERE ref=?`, ref)
	}
}
