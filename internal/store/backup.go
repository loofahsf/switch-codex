package store

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"
)

const (
	backupMagic     = "SCBACKUP"
	BackupExtension = ".scbackup"
	MaxBackupBytes  = 64 * 1024 * 1024

	backupVersion   = uint16(1)
	backupSaltSize  = 16
	backupNonceSize = 12
	backupKeySize   = 32
	backupHeaderLen = len(backupMagic) + 2 + backupSaltSize + backupNonceSize
	backupSchema    = 1
)

type backupAccount struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	Auth      []byte `json:"auth"`
}

type backupPayload struct {
	SchemaVersion    int             `json:"schemaVersion"`
	ExportedAt       string          `json:"exportedAt"`
	SourceAppVersion string          `json:"sourceAppVersion"`
	ActiveAccountID  *string         `json:"activeAccountId"`
	Accounts         []backupAccount `json:"accounts"`
}

// AccountsBackup is an already decrypted and validated account backup. Its
// contents remain private to this package so renderer bindings cannot expose
// credentials by accident.
type AccountsBackup struct{ payload backupPayload }

func (b *AccountsBackup) AccountCount() int {
	if b == nil {
		return 0
	}
	return len(b.payload.Accounts)
}

func (b *AccountsBackup) Clear() {
	if b == nil {
		return
	}
	for pos := range b.payload.Accounts {
		clearBytes(b.payload.Accounts[pos].Auth)
	}
	b.payload = backupPayload{}
}

type pendingImport struct {
	ExpectedIndexSHA256 string   `json:"expectedIndexSha256"`
	AccountIDs          []string `json:"accountIds"`
}

func validatePassphrase(passphrase string) error {
	if utf8.RuneCountInString(passphrase) < 8 {
		return errors.New("备份密码至少需要 8 个字符")
	}
	if len(passphrase) > 1024 {
		return errors.New("备份密码不能超过 1024 字节")
	}
	return nil
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func (s *Store) CreateAccountsBackup(passphrase, sourceVersion string) ([]byte, int, error) {
	if err := validatePassphrase(passphrase); err != nil {
		return nil, 0, err
	}
	payload, err := s.backupSnapshot(sourceVersion)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		for pos := range payload.Accounts {
			clearBytes(payload.Accounts[pos].Auth)
		}
	}()
	result, err := encryptAccountsBackup(payload, passphrase, rand.Reader)
	return result, len(payload.Accounts), err
}

func (s *Store) backupSnapshot(sourceVersion string) (backupPayload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.read()
	if err != nil {
		return backupPayload{}, err
	}
	if len(i.Accounts) == 0 {
		return backupPayload{}, errors.New("没有可导出的账号")
	}
	payload := backupPayload{
		SchemaVersion:    backupSchema,
		ExportedAt:       s.now().UTC().Format(time.RFC3339Nano),
		SourceAppVersion: strings.TrimSpace(sourceVersion),
		ActiveAccountID:  copyStringPtr(i.ActiveAccountID),
		Accounts:         make([]backupAccount, 0, len(i.Accounts)),
	}
	for _, account := range i.Accounts {
		auth, readErr := os.ReadFile(s.authPath(account.ID))
		if readErr != nil {
			return backupPayload{}, fmt.Errorf("无法读取账号 %q 的认证文件", account.Name)
		}
		payload.Accounts = append(payload.Accounts, backupAccount{
			ID: account.ID, Name: account.Name, CreatedAt: account.CreatedAt,
			UpdatedAt: account.UpdatedAt, Auth: bytes.Clone(auth),
		})
	}
	if err = validateBackupPayload(&payload); err != nil {
		return backupPayload{}, fmt.Errorf("账号数据无法导出: %w", err)
	}
	return payload, nil
}

func encryptAccountsBackup(payload backupPayload, passphrase string, random io.Reader) ([]byte, error) {
	if err := validatePassphrase(passphrase); err != nil {
		return nil, err
	}
	plain, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("无法序列化账号备份")
	}
	defer clearBytes(plain)
	header := make([]byte, backupHeaderLen)
	copy(header, backupMagic)
	binary.BigEndian.PutUint16(header[len(backupMagic):], backupVersion)
	saltStart := len(backupMagic) + 2
	nonceStart := saltStart + backupSaltSize
	if _, err = io.ReadFull(random, header[saltStart:nonceStart]); err != nil {
		return nil, errors.New("无法生成备份加密盐")
	}
	if _, err = io.ReadFull(random, header[nonceStart:]); err != nil {
		return nil, errors.New("无法生成备份加密随机数")
	}
	passwordBytes := []byte(passphrase)
	defer clearBytes(passwordBytes)
	key := argon2.IDKey(passwordBytes, header[saltStart:nonceStart], 3, 64*1024, 4, backupKeySize)
	defer clearBytes(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("无法初始化备份加密")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("无法初始化备份认证加密")
	}
	result := append(header, gcm.Seal(nil, header[nonceStart:], plain, header)...)
	if len(result) > MaxBackupBytes {
		clearBytes(result)
		return nil, errors.New("账号备份超过 64 MiB 安全限制")
	}
	return result, nil
}

func DecodeAccountsBackup(raw []byte, passphrase string) (*AccountsBackup, error) {
	if err := validatePassphrase(passphrase); err != nil {
		return nil, err
	}
	if len(raw) > MaxBackupBytes {
		return nil, errors.New("账号备份超过 64 MiB 安全限制")
	}
	if len(raw) < backupHeaderLen+16 || string(raw[:len(backupMagic)]) != backupMagic {
		return nil, errors.New("密码错误或备份文件已损坏")
	}
	version := binary.BigEndian.Uint16(raw[len(backupMagic):])
	if version != backupVersion {
		if version > backupVersion {
			return nil, errors.New("备份格式版本过新，请升级 Switch Codex")
		}
		return nil, errors.New("不支持该备份格式版本")
	}
	header := raw[:backupHeaderLen]
	saltStart := len(backupMagic) + 2
	nonceStart := saltStart + backupSaltSize
	passwordBytes := []byte(passphrase)
	defer clearBytes(passwordBytes)
	key := argon2.IDKey(passwordBytes, header[saltStart:nonceStart], 3, 64*1024, 4, backupKeySize)
	defer clearBytes(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("密码错误或备份文件已损坏")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("密码错误或备份文件已损坏")
	}
	plain, err := gcm.Open(nil, header[nonceStart:], raw[backupHeaderLen:], header)
	if err != nil {
		return nil, errors.New("密码错误或备份文件已损坏")
	}
	defer clearBytes(plain)
	var payload backupPayload
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&payload) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("密码错误或备份文件已损坏")
	}
	if payload.SchemaVersion != backupSchema {
		if payload.SchemaVersion > backupSchema {
			return nil, errors.New("备份内容版本过新，请升级 Switch Codex")
		}
		return nil, errors.New("不支持该备份内容版本")
	}
	if err = validateBackupPayload(&payload); err != nil {
		return nil, fmt.Errorf("备份账号数据无效: %w", err)
	}
	return &AccountsBackup{payload: payload}, nil
}

func validateBackupPayload(payload *backupPayload) error {
	if payload.SchemaVersion != backupSchema {
		return errors.New("内容版本无效")
	}
	if _, err := time.Parse(time.RFC3339Nano, payload.ExportedAt); err != nil {
		return errors.New("导出时间无效")
	}
	if len(payload.Accounts) == 0 {
		return errors.New("备份中没有账号")
	}
	seenIDs := make(map[string]struct{}, len(payload.Accounts))
	seenNames := make([]string, 0, len(payload.Accounts))
	seenIdentities := make(map[CredentialIdentity]struct{}, len(payload.Accounts))
	for pos := range payload.Accounts {
		account := &payload.Accounts[pos]
		if !validAccountID(account.ID) {
			return errors.New("备份包含非法账号 ID")
		}
		if _, ok := seenIDs[account.ID]; ok {
			return errors.New("备份包含重复账号 ID")
		}
		seenIDs[account.ID] = struct{}{}
		account.Name = strings.TrimSpace(account.Name)
		if account.Name == "" {
			return errors.New("备份包含空账号名称")
		}
		for _, name := range seenNames {
			if strings.EqualFold(name, account.Name) {
				return errors.New("备份包含重复账号名称")
			}
		}
		seenNames = append(seenNames, account.Name)
		if _, err := time.Parse(time.RFC3339Nano, account.CreatedAt); err != nil {
			return fmt.Errorf("账号 %q 的创建时间无效", account.Name)
		}
		if _, err := time.Parse(time.RFC3339Nano, account.UpdatedAt); err != nil {
			return fmt.Errorf("账号 %q 的更新时间无效", account.Name)
		}
		normalized, err := NormalizeAuth(account.Auth)
		if err != nil {
			return fmt.Errorf("账号 %q 的认证文件无效", account.Name)
		}
		account.Auth = normalized
		if identity, identityErr := IdentifyAuth(normalized); identityErr == nil {
			if _, ok := seenIdentities[identity]; ok {
				return errors.New("备份包含重复登录身份")
			}
			seenIdentities[identity] = struct{}{}
		}
	}
	if payload.ActiveAccountID != nil {
		if _, ok := seenIDs[*payload.ActiveAccountID]; !ok {
			return errors.New("备份的当前账号不存在")
		}
	}
	return nil
}

func validAccountID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, "/\\")
}

func copyStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func marshalIndex(value index) ([]byte, error) {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func (s *Store) pendingImportPath() string {
	return filepath.Join(s.DataDir, "accounts-import.pending.json")
}

func (s *Store) ImportAccountsBackup(backup *AccountsBackup) (AccountsState, error) {
	if backup == nil {
		return AccountsState{}, errors.New("账号备份为空")
	}
	payload := backup.payload
	if err := validateBackupPayload(&payload); err != nil {
		return AccountsState{}, fmt.Errorf("备份账号数据无效: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.read()
	if err != nil {
		return AccountsState{}, err
	}
	if len(current.Accounts) != 0 {
		return AccountsState{}, errors.New("仅允许导入到没有账号的目标库")
	}
	if _, err = os.Stat(s.pendingImportPath()); err == nil {
		return AccountsState{}, errors.New("存在尚未恢复的账号导入事务，请重启应用后重试")
	} else if !errors.Is(err, os.ErrNotExist) {
		return AccountsState{}, err
	}

	accounts := make([]Account, 0, len(payload.Accounts))
	authByID := make(map[string][]byte, len(payload.Accounts))
	defer func() {
		for _, auth := range authByID {
			clearBytes(auth)
		}
	}()
	ids := make([]string, 0, len(payload.Accounts))
	for _, imported := range payload.Accounts {
		id := uuid.NewString()
		if _, statErr := os.Stat(s.authPath(id)); !errors.Is(statErr, os.ErrNotExist) {
			if statErr == nil {
				return AccountsState{}, errors.New("无法分配安全的目标账号 ID")
			}
			return AccountsState{}, statErr
		}
		accounts = append(accounts, Account{ID: id, Name: imported.Name, CreatedAt: imported.CreatedAt, UpdatedAt: imported.UpdatedAt})
		authByID[id] = bytes.Clone(imported.Auth)
		ids = append(ids, id)
	}
	next := index{ActiveAccountID: nil, Accounts: accounts}
	indexBytes, err := marshalIndex(next)
	if err != nil {
		return AccountsState{}, err
	}
	digest := sha256.Sum256(indexBytes)
	pending := pendingImport{ExpectedIndexSHA256: hex.EncodeToString(digest[:]), AccountIDs: ids}
	pendingBytes, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return AccountsState{}, err
	}
	if err = s.write(s.pendingImportPath(), append(pendingBytes, '\n'), 0600); err != nil {
		return AccountsState{}, errors.New("无法创建账号导入事务")
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		for _, id := range ids {
			_ = os.RemoveAll(filepath.Dir(s.authPath(id)))
		}
		_ = os.Remove(s.pendingImportPath())
	}()
	for _, id := range ids {
		if err = s.write(s.authPath(id), authByID[id], 0600); err != nil {
			return AccountsState{}, errors.New("无法写入导入的账号凭据")
		}
	}
	if err = s.write(s.indexPath(), indexBytes, 0600); err != nil {
		return AccountsState{}, errors.New("无法提交导入的账号索引")
	}
	committed = true
	_ = os.Remove(s.pendingImportPath())
	return s.state(next), nil
}

func (s *Store) recoverPendingImport() error {
	raw, err := os.ReadFile(s.pendingImportPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("无法读取未完成的账号导入事务")
	}
	var pending pendingImport
	if json.Unmarshal(raw, &pending) != nil || len(pending.AccountIDs) == 0 {
		return errors.New("未完成的账号导入事务已损坏")
	}
	seen := make(map[string]struct{}, len(pending.AccountIDs))
	for _, id := range pending.AccountIDs {
		if !validAccountID(id) {
			return errors.New("未完成的账号导入事务包含非法 ID")
		}
		if _, ok := seen[id]; ok {
			return errors.New("未完成的账号导入事务包含重复 ID")
		}
		seen[id] = struct{}{}
	}
	indexRaw, readErr := os.ReadFile(s.indexPath())
	if readErr == nil {
		digest := sha256.Sum256(indexRaw)
		if hex.EncodeToString(digest[:]) == pending.ExpectedIndexSHA256 {
			return os.Remove(s.pendingImportPath())
		}
		var current index
		if json.Unmarshal(indexRaw, &current) != nil {
			return errors.New("账号索引损坏，无法恢复未完成的导入")
		}
		for _, account := range current.Accounts {
			if _, ok := seen[account.ID]; ok {
				return errors.New("账号导入事务与当前索引不一致，未自动删除凭据")
			}
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	for _, id := range pending.AccountIDs {
		if err = os.RemoveAll(filepath.Dir(s.authPath(id))); err != nil {
			return errors.New("无法清理未完成的账号导入")
		}
	}
	return os.Remove(s.pendingImportPath())
}
