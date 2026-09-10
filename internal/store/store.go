// Package store owns account metadata, credentials and atomic account switching.
package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
	"switch-codex/internal/platform"
	"sync"
	"time"
)

const BackupName = "auth.json.switch-codex.bak"

type Account struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}
type index struct {
	ActiveAccountID *string   `json:"activeAccountId"`
	Accounts        []Account `json:"accounts"`
}
type AccountItem struct {
	Account
	AuthPath string `json:"authPath"`
	IsActive bool   `json:"isActive"`
}
type AccountsState struct {
	DataDir         string        `json:"dataDir"`
	TargetAuthPath  string        `json:"targetAuthPath"`
	ActiveAccountID *string       `json:"activeAccountId"`
	Accounts        []AccountItem `json:"accounts"`
}
type CredentialIdentity struct {
	mode, userID, workspaceID string
	apiKeyHash                [sha256.Size]byte
}

func (i CredentialIdentity) Equal(other CredentialIdentity) bool { return i == other }

type ReconcileOutcome string

const (
	ReconcileUpToDate        ReconcileOutcome = "up_to_date"
	ReconcileSynced          ReconcileOutcome = "synced"
	ReconcileFollowed        ReconcileOutcome = "followed"
	ReconcileUnknown         ReconcileOutcome = "unknown"
	ReconcileAmbiguous       ReconcileOutcome = "ambiguous"
	ReconcileIdentityMissing ReconcileOutcome = "identity_missing"
)

type ReconcileResult struct {
	Outcome     ReconcileOutcome
	AccountID   *string
	AccountName *string
	State       *AccountsState
}

// Snapshot cannot be marshalled into a renderer payload; credentials remain private.
type Snapshot struct {
	ID, Name string
	auth     []byte
	err      error
}

func (s Snapshot) Credentials() ([]byte, error) { return bytes.Clone(s.auth), s.err }

type Store struct {
	mu                      sync.Mutex
	DataDir, TargetAuthPath string
	now                     func() time.Time
	write                   func(string, []byte, os.FileMode) error
}

func New(dataDir, targetAuthPath string) *Store {
	return &Store{DataDir: dataDir, TargetAuthPath: targetAuthPath, now: time.Now, write: platform.WriteAtomic}
}
func (s *Store) indexPath() string { return filepath.Join(s.DataDir, "accounts.json") }
func (s *Store) authPath(id string) string {
	return filepath.Join(s.DataDir, "accounts", id, "auth.json")
}
func (s *Store) EnsureReady() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(s.DataDir, "accounts"), 0700); err != nil {
		return err
	}
	if _, err := os.Stat(s.indexPath()); errors.Is(err, os.ErrNotExist) {
		return s.save(index{Accounts: []Account{}})
	} else if err != nil {
		return err
	}
	_, err := s.read()
	return err
}
func (s *Store) read() (index, error) {
	var i index
	b, err := os.ReadFile(s.indexPath())
	if err != nil {
		return i, err
	}
	if err = json.Unmarshal(b, &i); err != nil {
		return i, err
	}
	if len(bytes.TrimSpace(b)) == 0 || bytes.TrimSpace(b)[0] != '{' {
		return i, errors.New("账号索引必须是 JSON 对象")
	}
	for _, a := range i.Accounts {
		if a.ID == "" || a.ID == "." || a.ID == ".." || strings.ContainsAny(a.ID, "/\\") {
			return i, errors.New("账号索引包含非法 ID")
		}
	}
	if i.Accounts == nil {
		i.Accounts = []Account{}
	}
	return i, nil
}
func (s *Store) save(i index) error {
	b, err := json.MarshalIndent(i, "", "  ")
	if err != nil {
		return err
	}
	return s.write(s.indexPath(), append(b, '\n'), 0600)
}
func (s *Store) state(i index) AccountsState {
	items := make([]AccountItem, 0, len(i.Accounts))
	for _, a := range i.Accounts {
		items = append(items, AccountItem{Account: a, AuthPath: s.authPath(a.ID), IsActive: i.ActiveAccountID != nil && *i.ActiveAccountID == a.ID})
	}
	return AccountsState{DataDir: s.DataDir, TargetAuthPath: s.TargetAuthPath, ActiveAccountID: i.ActiveAccountID, Accounts: items}
}
func (s *Store) ListAccounts() (AccountsState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.read()
	return s.state(i), err
}
func (s *Store) AddAccount(name, authJSON string) (AccountsState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	normalized, err := NormalizeAuth([]byte(authJSON))
	if err != nil {
		return AccountsState{}, err
	}
	return s.addNormalizedAccount(name, normalized, false)
}

// AddCurrentAccount stores a credential that is already active without
// rewriting TargetAuthPath or its backup.
func (s *Store) AddCurrentAccount(name string, auth []byte) (AccountsState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	normalized, err := NormalizeAuth(auth)
	if err != nil {
		return AccountsState{}, err
	}
	return s.addNormalizedAccount(name, normalized, true)
}

func (s *Store) addNormalizedAccount(name string, normalized []byte, current bool) (AccountsState, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return AccountsState{}, errors.New("账号名字不能为空")
	}
	i, err := s.read()
	if err != nil {
		return AccountsState{}, err
	}
	for _, a := range i.Accounts {
		if strings.EqualFold(a.Name, name) {
			return AccountsState{}, errors.New("已经存在同名账号")
		}
	}
	if identity, identityErr := IdentifyAuth(normalized); identityErr == nil {
		for _, account := range i.Accounts {
			stored, readErr := os.ReadFile(s.authPath(account.ID))
			if readErr != nil {
				continue
			}
			storedIdentity, storedErr := IdentifyAuth(stored)
			if storedErr == nil && identity.Equal(storedIdentity) {
				return AccountsState{}, errors.New("该登录身份已经保存")
			}
		}
	}
	id := uuid.NewString()
	now := s.now().UTC().Format(time.RFC3339Nano)
	a := Account{ID: id, Name: name, CreatedAt: now, UpdatedAt: now}
	if err = s.write(s.authPath(id), normalized, 0600); err != nil {
		return AccountsState{}, err
	}
	i.Accounts = append(i.Accounts, a)
	if current {
		i.ActiveAccountID = &id
		err = s.save(i)
	} else if i.ActiveAccountID == nil {
		i.ActiveAccountID = &id
		err = s.activateAndSave(i, normalized)
	} else {
		err = s.save(i)
	}
	if err != nil {
		_ = os.RemoveAll(filepath.Dir(s.authPath(id)))
		return AccountsState{}, err
	}
	return s.state(i), nil
}
func (s *Store) RemoveAccount(id string) (AccountsState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.read()
	if err != nil {
		return AccountsState{}, err
	}
	pos := find(i, id)
	if pos < 0 {
		return AccountsState{}, errors.New("账号不存在")
	}
	// Rename to a tombstone first so a failed index write can restore the account.
	dir := filepath.Dir(s.authPath(id))
	tomb := dir + ".deleted-" + uuid.NewString()
	moved := false
	if _, err = os.Stat(dir); err == nil {
		if err = os.Rename(dir, tomb); err != nil {
			return AccountsState{}, err
		}
		moved = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return AccountsState{}, err
	}
	i.Accounts = append(i.Accounts[:pos], i.Accounts[pos+1:]...)
	if i.ActiveAccountID != nil && *i.ActiveAccountID == id {
		i.ActiveAccountID = nil
	}
	if err = s.save(i); err != nil {
		if moved {
			_ = os.Rename(tomb, dir)
		}
		return AccountsState{}, err
	}
	if moved {
		if err = os.RemoveAll(tomb); err != nil {
			return s.state(i), fmt.Errorf("账号已移除，但清理凭证目录失败: %w", err)
		}
	}
	return s.state(i), nil
}
func find(i index, id string) int {
	for n, a := range i.Accounts {
		if a.ID == id {
			return n
		}
	}
	return -1
}
func (s *Store) SwitchAccount(id string) (AccountsState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.read()
	if err != nil {
		return AccountsState{}, err
	}
	if find(i, id) < 0 {
		return AccountsState{}, errors.New("账号不存在")
	}
	b, err := os.ReadFile(s.authPath(id))
	if err != nil {
		return AccountsState{}, err
	}
	b, err = NormalizeAuth(b)
	if err != nil {
		return AccountsState{}, err
	}
	i.ActiveAccountID = &id
	if err = s.activateAndSave(i, b); err != nil {
		return AccountsState{}, err
	}
	return s.state(i), nil
}
func (s *Store) activateAndSave(i index, auth []byte) error {
	old, err := os.ReadFile(s.TargetAuthPath)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if existed {
		if err = s.write(filepath.Join(filepath.Dir(s.TargetAuthPath), BackupName), old, 0600); err != nil {
			return err
		}
	}
	if err = s.write(s.TargetAuthPath, auth, 0600); err != nil {
		return err
	}
	if err = s.save(i); err != nil {
		var rollback error
		if existed {
			rollback = s.write(s.TargetAuthPath, old, 0600)
		} else {
			rollback = os.Remove(s.TargetAuthPath)
		}
		if rollback != nil {
			return fmt.Errorf("保存账号索引失败: %w；恢复原凭证失败，请使用备份恢复: %v", err, rollback)
		}
		return err
	}
	return nil
}

// ReconcileTargetAuth safely associates the active Codex credential with one
// stored account. It never writes TargetAuthPath.
func (s *Store) ReconcileTargetAuth(auth []byte) (ReconcileResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	normalized, err := NormalizeAuth(auth)
	if err != nil {
		return ReconcileResult{}, err
	}
	identity, err := IdentifyAuth(normalized)
	if err != nil {
		return ReconcileResult{Outcome: ReconcileIdentityMissing}, nil
	}
	i, err := s.read()
	if err != nil {
		return ReconcileResult{}, err
	}
	type match struct {
		pos       int
		raw, auth []byte
	}
	matches := make([]match, 0, 1)
	for pos, account := range i.Accounts {
		raw, readErr := os.ReadFile(s.authPath(account.ID))
		if readErr != nil {
			continue
		}
		stored, normalizeErr := NormalizeAuth(raw)
		if normalizeErr != nil {
			continue
		}
		storedIdentity, identityErr := IdentifyAuth(stored)
		if identityErr == nil && identity.Equal(storedIdentity) {
			matches = append(matches, match{pos: pos, raw: raw, auth: stored})
		}
	}
	if len(matches) == 0 {
		return ReconcileResult{Outcome: ReconcileUnknown}, nil
	}
	if len(matches) > 1 {
		return ReconcileResult{Outcome: ReconcileAmbiguous}, nil
	}
	m := matches[0]
	account := i.Accounts[m.pos]
	accountID, accountName := account.ID, account.Name
	result := ReconcileResult{Outcome: ReconcileUpToDate, AccountID: &accountID, AccountName: &accountName}
	isActive := i.ActiveAccountID != nil && *i.ActiveAccountID == account.ID
	authChanged := !bytes.Equal(m.auth, normalized)
	if !authChanged && isActive {
		return result, nil
	}
	if err = s.updateSavedAuthAndActive(&i, m.pos, m.raw, normalized, authChanged, !isActive); err != nil {
		return ReconcileResult{}, err
	}
	state := s.state(i)
	result.State = &state
	if !isActive {
		result.Outcome = ReconcileFollowed
	} else {
		result.Outcome = ReconcileSynced
	}
	return result, nil
}
func (s *Store) updateSavedAuth(i *index, pos int, old, updated []byte) error {
	return s.updateSavedAuthAndActive(i, pos, old, updated, true, false)
}

func (s *Store) updateSavedAuthAndActive(i *index, pos int, old, updated []byte, updateAuth, setActive bool) error {
	path := s.authPath(i.Accounts[pos].ID)
	if updateAuth {
		if err := s.write(path, updated, 0600); err != nil {
			return err
		}
		i.Accounts[pos].UpdatedAt = s.now().UTC().Format(time.RFC3339Nano)
	}
	if setActive {
		id := i.Accounts[pos].ID
		i.ActiveAccountID = &id
	}
	if err := s.save(*i); err != nil {
		if updateAuth {
			if restore := s.write(path, old, 0600); restore != nil {
				return fmt.Errorf("保存索引失败: %w；恢复原凭证失败: %v", err, restore)
			}
		}
		return err
	}
	return nil
}
func (s *Store) Snapshots() ([]Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.read()
	if err != nil {
		return nil, err
	}
	result := make([]Snapshot, 0, len(i.Accounts))
	for _, a := range i.Accounts {
		b, err := os.ReadFile(s.authPath(a.ID))
		if err != nil {
			err = errors.New("无法读取账号认证文件")
		}
		result = append(result, Snapshot{ID: a.ID, Name: a.Name, auth: b, err: err})
	}
	return result, nil
}
func (s *Store) PersistRefreshedAuth(id string, original, refreshed []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if bytes.Equal(original, refreshed) {
		return nil
	}
	normalized, err := NormalizeAuth(refreshed)
	if err != nil {
		return err
	}
	a, aErr := IdentifyAuth(original)
	b, bErr := IdentifyAuth(normalized)
	if aErr != nil || bErr != nil || !a.Equal(b) {
		return errors.New("刷新后的认证身份无法确认，未覆盖已保存凭证")
	}
	i, err := s.read()
	if err != nil {
		return err
	}
	pos := find(i, id)
	if pos < 0 {
		return errors.New("账号已删除，未回写刷新凭证")
	}
	current, err := os.ReadFile(s.authPath(id))
	if err != nil {
		return errors.New("无法读取待回写凭证")
	}
	if !bytes.Equal(current, original) {
		return errors.New("账号凭证已更新，未覆盖新的凭证")
	}
	active := i.ActiveAccountID != nil && *i.ActiveAccountID == id
	if !active {
		return s.updateSavedAuth(&i, pos, current, normalized)
	}
	target, err := os.ReadFile(s.TargetAuthPath)
	if err != nil || !bytes.Equal(target, original) {
		return errors.New("当前认证文件已更新，未覆盖新的凭证")
	}
	backup := filepath.Join(filepath.Dir(s.TargetAuthPath), BackupName)
	if err = s.write(backup, target, 0600); err != nil {
		return err
	}
	if err = s.write(s.TargetAuthPath, normalized, 0600); err != nil {
		return err
	}
	if err = s.updateSavedAuth(&i, pos, current, normalized); err != nil {
		if restore := s.write(s.TargetAuthPath, target, 0600); restore != nil {
			return fmt.Errorf("%w；恢复当前凭证失败: %v", err, restore)
		}
		return err
	}
	return nil
}
func NormalizeAuth(raw []byte) ([]byte, error) {
	var obj map[string]json.RawMessage
	if !json.Valid(raw) {
		return nil, errors.New("auth.json 内容不是合法 JSON")
	}
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, errors.New("auth.json 必须是 JSON 对象")
	}
	var key string
	_ = json.Unmarshal(obj["OPENAI_API_KEY"], &key)
	var tokens struct {
		RefreshToken string `json:"refresh_token"`
	}
	_ = json.Unmarshal(obj["tokens"], &tokens)
	if strings.TrimSpace(key) == "" && strings.TrimSpace(tokens.RefreshToken) == "" {
		return nil, errors.New("auth.json 缺少 Codex 登录凭据")
	}
	// RawMessage keeps unknown fields and large integer values lossless.
	b, err := json.MarshalIndent(obj, "", "  ")
	return append(b, '\n'), err
}
func AccountID(raw []byte) *string {
	var v struct {
		Tokens struct {
			AccountID string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	id := strings.TrimSpace(v.Tokens.AccountID)
	if id == "" {
		return nil
	}
	return &id
}

func IdentifyAuth(raw []byte) (CredentialIdentity, error) {
	var v struct {
		APIKey string `json:"OPENAI_API_KEY"`
		Tokens struct {
			AccountID   string `json:"account_id"`
			IDToken     string `json:"id_token"`
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return CredentialIdentity{}, errors.New("无法识别认证身份")
	}
	if key := strings.TrimSpace(v.APIKey); key != "" {
		return CredentialIdentity{mode: "api_key", apiKeyHash: sha256.Sum256([]byte(key))}, nil
	}
	workspaceID := strings.TrimSpace(v.Tokens.AccountID)
	var userID, tokenWorkspaceID string
	for _, token := range []string{v.Tokens.IDToken, v.Tokens.AccessToken} {
		user, workspace, ok := jwtIdentity(token)
		if !ok {
			continue
		}
		if userID != "" && user != "" && userID != user {
			return CredentialIdentity{}, errors.New("认证 Token 的用户身份不一致")
		}
		if tokenWorkspaceID != "" && workspace != "" && tokenWorkspaceID != workspace {
			return CredentialIdentity{}, errors.New("认证 Token 的工作空间不一致")
		}
		if userID == "" {
			userID = user
		}
		if tokenWorkspaceID == "" {
			tokenWorkspaceID = workspace
		}
	}
	if workspaceID == "" {
		workspaceID = tokenWorkspaceID
	} else if tokenWorkspaceID != "" && workspaceID != tokenWorkspaceID {
		return CredentialIdentity{}, errors.New("auth.json 的工作空间身份不一致")
	}
	if userID == "" || workspaceID == "" {
		return CredentialIdentity{}, errors.New("auth.json 缺少可安全识别的用户或工作空间身份")
	}
	return CredentialIdentity{mode: "chatgpt", userID: userID, workspaceID: workspaceID}, nil
}

func jwtIdentity(token string) (string, string, bool) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return "", "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", "", false
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return "", "", false
	}
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	user := firstClaim(auth, "chatgpt_user_id")
	if user == "" {
		user = firstClaim(claims, "chatgpt_user_id", "sub")
	}
	workspace := firstClaim(auth, "chatgpt_account_id", "chatgpt_workspace_id")
	if workspace == "" {
		workspace = firstClaim(claims, "chatgpt_account_id", "chatgpt_workspace_id")
	}
	return user, workspace, user != "" || workspace != ""
}

func firstClaim(claims map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := claims[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
